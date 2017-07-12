package stripe

import (
	"context"
	"net/http"

	"encoding/json"

	"github.com/netlify/gocommerce/payments"
	"github.com/pkg/errors"
	stripe "github.com/stripe/stripe-go"
	"github.com/stripe/stripe-go/client"
)

type stripePaymentProvider struct {
	client *client.API
}

type stripeRequestHandler struct {
	p     *stripePaymentProvider
	token string
}

type stripeBodyParams struct {
	StripeToken string `mapstructure:"stripe_token"`
}

// Config contains the Stripe-specific configuration for payment providers.
type Config struct {
	SecretKey string `mapstructure:"secret_key" json:"secret_key"`
}

func NewPaymentProvider(config Config) (payments.Provider, error) {
	if config.SecretKey == "" {
		return nil, errors.New("Stripe configuration missing secret_key")
	}

	s := stripePaymentProvider{
		client: &client.API{},
	}
	s.client.Init(config.SecretKey, nil)
	return &s, nil
}

func (s *stripePaymentProvider) Name() string {
	return payments.StripeProvider
}

func (s *stripePaymentProvider) NewCharger(ctx context.Context, r *http.Request) (payments.Charger, error) {
	var bp stripeBodyParams
	bod, err := r.GetBody()
	if err != nil {
		return nil, err
	}
	err = json.NewDecoder(bod).Decode(&bp)
	if err != nil {
		return nil, err
	}
	if bp.StripeToken == "" {
		return nil, errors.New("Stripe requires a stripe_token for creating a payment")
	}

	return &stripeRequestHandler{
		p:     s,
		token: bp.StripeToken,
	}, nil
}

func (r *stripeRequestHandler) Charge(amount uint64, currency string) (string, error) {
	ch, err := r.p.client.Charges.New(&stripe.ChargeParams{
		Amount:   amount,
		Source:   &stripe.SourceParams{Token: r.token},
		Currency: stripe.Currency(currency),
	})

	if err != nil {
		return "", err
	}

	return ch.ID, nil
}

func (s *stripePaymentProvider) NewRefunder(ctx context.Context, r *http.Request) (payments.Refunder, error) {
	return &stripeRequestHandler{
		p: s,
	}, nil
}

func (r *stripeRequestHandler) Refund(transactionID string, amount uint64) (string, error) {
	ref, err := r.p.client.Refunds.New(&stripe.RefundParams{
		Charge: transactionID,
		Amount: amount,
	})
	if err != nil {
		return "", err
	}

	return ref.ID, err
}

func (s *stripePaymentProvider) NewPreauthorizer(ctx context.Context, r *http.Request) (payments.Preauthorizer, error) {
	return nil, errors.New("Stripe does not require preauthorization")
}
