package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"strings"

	"github.com/go-chi/chi"

	"github.com/Sirupsen/logrus"
	"github.com/jinzhu/gorm"
	"github.com/pborman/uuid"

	"github.com/netlify/gocommerce/claims"
	"github.com/netlify/gocommerce/conf"
	gcontext "github.com/netlify/gocommerce/context"
	"github.com/netlify/gocommerce/models"
	"github.com/netlify/gocommerce/payments"
	"github.com/netlify/gocommerce/payments/paypal"
	"github.com/netlify/gocommerce/payments/stripe"
)

// PaymentParams holds the parameters for creating a payment
type PaymentParams struct {
	Amount       uint64 `json:"amount"`
	Currency     string `json:"currency"`
	ProviderType string `json:"provider"`
}

// PaymentListForUser is the endpoint for listing transactions for a user.
// The ID in the claim and the ID in the path must match (or have admin override)
func (a *API) PaymentListForUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := getLogEntry(r)
	userID := getUserID(ctx)

	trans, httpErr := queryForTransactions(a.db, log, "user_id = ?", userID)
	if httpErr != nil {
		sendJSON(w, httpErr.Code, httpErr)
		return
	}
	sendJSON(w, http.StatusOK, trans)
}

// PaymentListForOrder is the endpoint for listing transactions for an order. You must be the owner
// of the order (user_id) or an admin. Listing the payments for an anon order.
func (a *API) PaymentListForOrder(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := getLogEntry(r)
	orderID := getOrderID(ctx)
	claims := gcontext.GetClaims(ctx)

	order, httpErr := queryForOrder(a.db, orderID, log)
	if httpErr != nil {
		sendJSON(w, httpErr.Code, httpErr)
		return
	}

	if !hasOrderAccess(ctx, order) {
		log.Warnf("Attempt to access order as %s, but order.UserID is %s", claims.ID, order.UserID)
		unauthorizedError(w, "You don't have access to this order")
		return
	}

	// additional check for anonymous orders: only allow admins
	isAdmin := gcontext.IsAdmin(ctx)
	if order.UserID == "" && !isAdmin {
		// anon order ~ only accessible by an admin
		log.Warn("Queried for an anonymous order but not as admin")
		unauthorizedError(w, "Anonymous orders must be accessed by admins")
		return
	}

	log.Debugf("Returning %d transactions", len(order.Transactions))
	sendJSON(w, http.StatusOK, order.Transactions)
}

// PaymentCreate is the endpoint for creating a payment for an order
func (a *API) PaymentCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	log := getLogEntry(r)
	config := gcontext.GetConfig(ctx)
	mailer := gcontext.GetMailer(ctx)

	params := PaymentParams{Currency: "USD"}
	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		badRequestError(w, "Could not read params: %v", err)
		return
	}
	if params.ProviderType == "" {
		badRequestError(w, "Creating a payment requires specifying a 'provider'")
		return
	}

	provider := gcontext.GetPaymentProviders(ctx)[strings.ToLower(params.ProviderType)]
	if provider == nil {
		badRequestError(w, "Payment provider '%s' not configured", params.ProviderType)
		return
	}
	charge, err := provider.NewCharger(ctx, r)
	if err != nil {
		badRequestError(w, "Error creating payment provider: %v", err)
		return
	}

	orderID := getOrderID(ctx)
	tx := a.db.Begin()
	order := &models.Order{}

	if result := tx.Preload("LineItems").Preload("BillingAddress").First(order, "id = ?", orderID); result.Error != nil {
		tx.Rollback()
		if result.RecordNotFound() {
			notFoundError(w, "No order with this ID found")
		} else {
			internalServerError(w, "Error during database query: %v", result.Error)
		}
		return
	}

	if order.PaymentState == models.PaidState {
		tx.Rollback()
		badRequestError(w, "This order has already been paid")
		return
	}

	if order.Currency != params.Currency {
		tx.Rollback()
		badRequestError(w, "Currencies doesn't match - %v vs %v", order.Currency, params.Currency)
		return
	}

	token := gcontext.GetToken(ctx)
	if order.UserID == "" {
		if token != nil {
			claims := token.Claims.(*claims.JWTClaims)
			order.UserID = claims.ID
			tx.Save(order)
		}
	} else {
		if token == nil {
			tx.Rollback()
			unauthorizedError(w, "You must be logged in to pay for this order")
			return
		}
		claims := token.Claims.(*claims.JWTClaims)
		if order.UserID != claims.ID {
			tx.Rollback()
			unauthorizedError(w, "You must be logged in to pay for this order")
			return
		}
	}

	err = a.verifyAmount(ctx, order, params.Amount)
	if err != nil {
		tx.Rollback()
		internalServerError(w, "We failed to authorize the amount for this order: %v", err)
		return
	}
	tr := models.NewTransaction(order)

	order.PaymentProcessor = provider.Name()
	processorID, err := charge(params.Amount, params.Currency)
	tr.ProcessorID = processorID

	if err != nil {
		tr.FailureCode = strconv.FormatInt(http.StatusInternalServerError, 10)
		tr.FailureDescription = err.Error()
		tr.Status = "failed"
	} else {
		tr.Status = "pending"
	}
	tx.Create(tr)

	if err != nil {
		tx.Commit()
		internalServerError(w, "There was an error charging your card: %v", err)
		return
	}

	order.PaymentState = models.PaidState
	tx.Save(order)

	if config.Webhooks.Payment != "" {
		hook := models.NewHook("payment", config.Webhooks.Payment, order.UserID, config.Webhooks.Secret, order)
		tx.Save(hook)
	}

	tx.Commit()

	go func() {
		err1 := mailer.OrderConfirmationMail(tr)
		err2 := mailer.OrderReceivedMail(tr)

		if err1 != nil || err2 != nil {
			log.Errorf("Error sending order confirmation mails: %v %v", err1, err2)
		}
	}()

	sendJSON(w, http.StatusOK, tr)
}

// PaymentList will list all the payments that meet the criteria. It is only available to admins.
func (a *API) PaymentList(w http.ResponseWriter, r *http.Request) {
	log := getLogEntry(r)
	query, err := parsePaymentQueryParams(a.db, r.URL.Query())
	if err != nil {
		log.WithError(err).Info("Malformed request")
		badRequestError(w, err.Error())
		return
	}

	trans, httpErr := queryForTransactions(query, log, "", "")
	if httpErr != nil {
		sendJSON(w, httpErr.Code, httpErr)
		return
	}
	sendJSON(w, http.StatusOK, trans)
}

// PaymentView returns information about a single payment. It is only available to admins.
func (a *API) PaymentView(w http.ResponseWriter, r *http.Request) {
	if trans, httpErr := a.getTransaction(r); httpErr != nil {
		sendJSON(w, httpErr.Code, httpErr)
	} else {
		sendJSON(w, http.StatusOK, trans)
	}
}

// PaymentRefund refunds a transaction for a specific amount. This allows partial
// refunds if desired. It is only available to admins.
func (a *API) PaymentRefund(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	config := gcontext.GetConfig(ctx)
	params := PaymentParams{Currency: "USD"}
	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		badRequestError(w, "Could not read params: %v", err)
		return
	}

	trans, httpErr := a.getTransaction(r)
	if httpErr != nil {
		sendJSON(w, httpErr.Code, httpErr)
		return
	}

	if trans.Currency != params.Currency {
		badRequestError(w, "Currencies do not match - %v vs %v", trans.Currency, params.Currency)
		return
	}

	if params.Amount <= 0 || params.Amount > trans.Amount {
		badRequestError(w, "The balance of the refund must be between 0 and the total amount")
		return
	}

	if trans.FailureCode != "" {
		badRequestError(w, "Can't refund a failed transaction")
		return
	}

	if trans.Status != models.PaidState {
		badRequestError(w, "Can't refund a transaction that hasn't been paid")
		return
	}

	log := getLogEntry(r)
	order, httpErr := queryForOrder(a.db, trans.OrderID, log)
	if httpErr != nil {
		sendJSON(w, httpErr.Code, httpErr)
		return
	}
	if order.PaymentProcessor == "" {
		internalServerError(w, "Order does not specify a payment provider")
		return
	}

	provider := gcontext.GetPaymentProviders(ctx)[order.PaymentProcessor]
	if provider == nil {
		badRequestError(w, "Payment provider '%s' not configured", order.PaymentProcessor)
		return
	}
	refund, err := provider.NewRefunder(ctx, r)
	if err != nil {
		badRequestError(w, "Error creating payment provider: %v", err)
		return
	}

	// ok make the refund
	m := &models.Transaction{
		ID:       uuid.NewRandom().String(),
		Amount:   params.Amount,
		Currency: params.Currency,
		UserID:   trans.UserID,
		OrderID:  trans.OrderID,
		Type:     models.RefundTransactionType,
		Status:   models.PendingState,
	}

	tx := a.db.Begin()
	tx.Create(m)
	provID := provider.Name()
	log.Debugf("Starting refund to %s", provID)
	refundID, err := refund(trans.ProcessorID, params.Amount, params.Currency)
	if err != nil {
		log.WithError(err).Info("Failed to refund value")
		m.FailureCode = strconv.FormatInt(http.StatusInternalServerError, 10)
		m.FailureDescription = err.Error()
		m.Status = models.FailedState
	} else {
		m.ProcessorID = refundID
		m.Status = models.PaidState
	}

	log.Infof("Finished transaction with %s: %s", provID, m.ProcessorID)
	tx.Save(m)
	if config.Webhooks.Refund != "" {
		hook := models.NewHook("refund", config.Webhooks.Refund, m.UserID, config.Webhooks.Secret, m)
		tx.Save(hook)
	}
	tx.Commit()
	sendJSON(w, http.StatusOK, m)
}

// PreauthorizePayment creates a new payment that can be authorized in the browser
func (a *API) PreauthorizePayment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	providerType := strings.ToLower(r.FormValue("provider"))
	if providerType == "" {
		badRequestError(w, "Preauthorizing a payment requires specifying a 'provider'")
		return
	}

	provider := gcontext.GetPaymentProviders(ctx)[providerType]
	if provider == nil {
		badRequestError(w, "Payment provider '%s' not configured", providerType)
		return
	}
	preauthorize, err := provider.NewPreauthorizer(ctx, r)
	if err != nil {
		badRequestError(w, "Error creating payment provider: %v", err)
		return
	}

	// TODO it is odd that this is the only method using FORM values
	amt, err := strconv.ParseUint(r.FormValue("amount"), 10, 64)
	if err != nil {
		internalServerError(w, "Error parsing amount: %v", err)
		return
	}

	paymentResult, err := preauthorize(amt, r.FormValue("currency"), r.FormValue("description"))
	if err != nil {
		internalServerError(w, "Error preauthorizing payment: %v", err)
		return
	}

	sendJSON(w, http.StatusOK, paymentResult)
}

// PayPalGetPayment retrieves information on an authorized paypal payment, including
// the shipping address
// func (a *API) PayPalGetPayment(w http.ResponseWriter, r *http.Request) {
//	ctx := r.Context()
// 	provider, ok := getPaymentProvider(ctx).(*payments.paypalPaymentProvider)
// 	if !ok {
// 		internalServerError(w, "PayPal provider not available")
// 		return
// 	}
// 	payment, err := provider.client.GetPayment(chi.URLParam(r, "payment_id"))
// 	if err != nil {
// 		internalServerError(w, "Error fetching paypal payment: %v", err)
// 		return
// 	}

// 	sendJSON(w, http.StatusOK, payment)
// }

// ------------------------------------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------------------------------------
func (a *API) getTransaction(r *http.Request) (*models.Transaction, *HTTPError) {
	payID := chi.URLParam(r, "payment_id")

	log := getLogEntry(r)
	trans := &models.Transaction{ID: payID}
	if rsp := a.db.First(trans); rsp.Error != nil {
		if rsp.RecordNotFound() {
			log.Infof("Failed to find transaction %s", payID)
			return nil, httpError(http.StatusNotFound, "Transaction not found")
		}

		log.WithError(rsp.Error).Warnf("Error while querying for transaction '%s'", payID)
		return nil, httpError(http.StatusInternalServerError, "Error while querying for transactions")
	}

	return trans, nil
}

func (a *API) verifyAmount(ctx context.Context, order *models.Order, amount uint64) error {
	if order.Total != amount {
		return fmt.Errorf("Amount calculated for order didn't match amount to charge. %v vs %v", order.Total, amount)
	}

	return nil
}

func queryForOrder(db *gorm.DB, orderID string, log logrus.FieldLogger) (*models.Order, *HTTPError) {
	order := &models.Order{}
	if rsp := db.Preload("Transactions").Find(order, "id = ?", orderID); rsp.Error != nil {
		if rsp.RecordNotFound() {
			log.Infof("Failed to find order %s", orderID)
			return nil, httpError(http.StatusNotFound, "Order not found")
		}

		log.WithError(rsp.Error).Warnf("Error while querying for order %s", orderID)
		return nil, httpError(http.StatusInternalServerError, "Error while querying for order")
	}
	return order, nil
}

func queryForTransactions(db *gorm.DB, log logrus.FieldLogger, clause, id string) ([]models.Transaction, *HTTPError) {
	trans := []models.Transaction{}
	if rsp := db.Find(&trans, clause, id); rsp.Error != nil {
		if rsp.RecordNotFound() {
			log.Infof("Failed to find transactions that meet criteria '%s' '%s'", clause, id)
			return nil, httpError(http.StatusNotFound, "Transactions not found")
		}

		log.WithError(rsp.Error).Warnf("Error while querying for transactions '%s' '%s'", clause, id)
		return nil, httpError(http.StatusInternalServerError, "Error while querying for transactions")
	}

	return trans, nil
}

// createPaymentProviders creates instance(s) of Provider based on the configuration
// provided.
func createPaymentProviders(c *conf.Configuration) (map[string]payments.Provider, error) {
	provs := map[string]payments.Provider{}
	if c.Payment.Stripe.Enabled {
		p, err := stripe.NewPaymentProvider(stripe.Config{
			SecretKey: c.Payment.Stripe.SecretKey,
		})
		if err != nil {
			return nil, err
		}
		provs[p.Name()] = p
	}
	if c.Payment.PayPal.Enabled {
		p, err := paypal.NewPaymentProvider(paypal.Config{
			Env:      c.Payment.PayPal.Env,
			ClientID: c.Payment.PayPal.ClientID,
			Secret:   c.Payment.PayPal.Secret,
		})
		if err != nil {
			return nil, err
		}
		provs[p.Name()] = p
	}
	return provs, nil
}
