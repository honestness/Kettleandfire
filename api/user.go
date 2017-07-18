package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"reflect"
	"time"

	"github.com/Sirupsen/logrus"
	"github.com/go-chi/chi"
	"github.com/jinzhu/gorm"
	"github.com/pborman/uuid"

	"github.com/netlify/gocommerce/models"
)

func (a *API) userCtx(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := chi.URLParam(r, "user_id")
		logEntrySetField(r, "user_id", userID)

		ctx := withUserID(r.Context(), userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserList will return all of the users. It requires admin access.
// It supports the filters:
// since     iso8601 date
// before		 iso8601 date
// email     email
// user_id   id
// limit     # of records to return (max)
func (a *API) UserList(w http.ResponseWriter, r *http.Request) {
	log := getLogEntry(r)

	query, err := parseUserQueryParams(a.db, r.URL.Query())
	if err != nil {
		log.WithError(err).Info("Bad query parameters in request")
		badRequestError(w, "Bad parameters in query: "+err.Error())
		return
	}

	log.Debug("Parsed url params")

	var users []models.User
	orderTable := models.Order{}.TableName()
	userTable := models.User{}.TableName()
	query = query.
		Joins("LEFT JOIN " + orderTable + " as orders ON " + userTable + ".id = orders.user_id").
		Select(userTable + ".id, " + userTable + ".email, " + userTable + ".created_at, " + userTable + ".updated_at, count(orders.id) as order_count").
		Group(userTable + ".id")

	offset, limit, err := paginate(w, r, query.Model(&models.User{}))
	if err != nil {
		if err == sql.ErrNoRows {
			sendJSON(w, http.StatusOK, []string{})
			return
		}
		badRequestError(w, "Bad Pagination Parameters: %v", err)
		return
	}

	rows, err := query.Offset(offset).Limit(limit).Find(&users).Rows()
	if err != nil {
		log.WithError(err).Warn("Error while querying the database")
		internalServerError(w, "Failed to execute request")
		return
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var id, email string
		var createdAt, updatedAt time.Time
		var orderCount int64
		err := rows.Scan(&id, &email, &createdAt, &updatedAt, &orderCount)
		if err != nil {
			log.WithError(err).Warn("Error while querying the database")
			internalServerError(w, "Failed to execute request")
			return
		}
		users[i].OrderCount = orderCount
		i++
	}

	numUsers := len(users)
	log.WithField("user_count", numUsers).Debugf("Successfully retrieved %d users", numUsers)
	sendJSON(w, http.StatusOK, users)
}

// UserView will return the user specified.
// If you're an admin you can request a user that is not your self
func (a *API) UserView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(ctx)
	log := getLogEntry(r)

	user := &models.User{
		ID: userID,
	}
	rsp := a.db.First(user)
	if rsp.RecordNotFound() {
		notFoundError(w, "Couldn't find a record for "+userID)
		return
	}

	if rsp.Error != nil {
		log.WithError(rsp.Error).Warnf("Failed to query DB: %v", rsp.Error)
		internalServerError(w, "Problem searching for user "+userID)
		return
	}

	if user.DeletedAt != nil {
		notFoundError(w, "Couldn't find a record for "+userID)
		return
	}

	orders := []models.Order{}
	a.db.Where("user_id = ?", user.ID).Find(&orders).Count(&user.OrderCount)

	sendJSON(w, http.StatusOK, user)
}

// AddressList will return the addresses for a given user
func (a *API) AddressList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(ctx)
	log := getLogEntry(r)

	if getUser(a.db, userID) == nil {
		log.WithError(notFoundError(w, "couldn't find a record for user: "+userID)).Warn("requested non-existent user")
		return
	}

	addrs := []models.Address{}
	results := a.db.Where("user_id = ?", userID).Find(&addrs)
	if results.Error != nil {
		log.WithError(results.Error).Warn("failed to query for userID: " + userID)
		internalServerError(w, "problem while querying for userID: "+userID)
		return
	}

	sendJSON(w, http.StatusOK, &addrs)
}

// AddressView will return a particular address for a given user
func (a *API) AddressView(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	addrID := chi.URLParam(r, "addr_id")
	userID := getUserID(ctx)
	log := getLogEntry(r)

	if getUser(a.db, userID) == nil {
		log.WithError(notFoundError(w, "couldn't find a record for user: "+userID)).Warn("requested non-existent user")
		return
	}

	addr := &models.Address{
		ID:     addrID,
		UserID: userID,
	}
	results := a.db.First(addr)
	if results.Error != nil {
		log.WithError(results.Error).Warn("failed to query for userID: " + userID)
		internalServerError(w, "problem while querying for userID: "+userID)
		return
	}

	sendJSON(w, http.StatusOK, &addr)
}

// UserDelete will soft delete the user. It requires admin access
// return errors or 200 and no body
func (a *API) UserDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(ctx)
	log := getLogEntry(r)
	log.Debugf("Starting to delete user %s", userID)

	user := getUser(a.db, userID)
	if user == nil {
		log.Info("attempted to delete non-existent user")
		return // not an error ~ just an action
	}

	// do a cascading delete
	tx := a.db.Begin()

	results := tx.Delete(user)
	if results.Error != nil {
		tx.Rollback()
		log.WithError(results.Error).Warn("Failed to find associated orders")
		internalServerError(w, "Failed to delete user")
		return
	}
	log.Debug("Deleted user")

	orders := []models.Order{}
	results = tx.Where("user_id = ?", userID).Find(&orders)
	if results.Error != nil {
		tx.Rollback()
		log.WithError(results.Error).Warn("Failed to find associated orders")
		internalServerError(w, "Failed to delete user")
		return
	}

	log.Debugf("Starting to collect info about %d orders", len(orders))
	orderIDs := []string{}
	for _, o := range orders {
		orderIDs = append(orderIDs, o.ID)
	}

	log.Debugf("Deleting line items")
	results = tx.Where("order_id in (?)", orderIDs).Delete(&models.LineItem{})
	if results.Error != nil {
		tx.Rollback()
		log.WithError(results.Error).
			WithField("order_ids", orderIDs).
			Warnf("Failed to delete line items associated with orders: %v", orderIDs)
		internalServerError(w, "Failed to delete user")
		return
	}
	log.Debugf("Deleted %d items", results.RowsAffected)

	if err := tryDelete(tx, w, log, userID, &models.Order{}); err != nil {
		return
	}
	if err := tryDelete(tx, w, log, userID, &models.Transaction{}); err != nil {
		return
	}
	if err := tryDelete(tx, w, log, userID, &models.OrderNote{}); err != nil {
		return
	}
	if err := tryDelete(tx, w, log, userID, &models.Address{}); err != nil {
		return
	}

	tx.Commit()
	log.Infof("Deleted user")
}

// AddressDelete will soft delete the address associated with that user. It requires admin access
// return errors or 200 and no body
func (a *API) AddressDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	addrID := chi.URLParam(r, "addr_id")
	userID := getUserID(ctx)
	log := getLogEntry(r).WithField("addr_id", addrID)

	if getUser(a.db, userID) == nil {
		log.Warn("requested non-existent user - not an error b/c it is a delete")
		return
	}

	rsp := a.db.Delete(&models.Address{ID: addrID})
	if rsp.RecordNotFound() {
		log.Warn("Attempted to delete an address that doesn't exist")
		return
	} else if rsp.Error != nil {
		log.WithError(rsp.Error).Warn("Error while deleting address")
		internalServerError(w, "error while deleting address")
		return
	}

	log.Info("deleted address")
}

// CreateNewAddress will create an address associated with that user
func (a *API) CreateNewAddress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := getUserID(ctx)
	log := getLogEntry(r)

	if getUser(a.db, userID) == nil {
		log.WithError(notFoundError(w, "Couldn't find user "+userID)).Warn("Requested to add an address to a missing user")
		return
	}

	addrReq := new(models.AddressRequest)
	err := json.NewDecoder(r.Body).Decode(addrReq)
	if err != nil {
		log.WithError(err).Info("Failed to parse json")
		badRequestError(w, "Failed to parse json body")
		return
	}

	if err := addrReq.Validate(); err != nil {
		log.WithError(err).Infof("requested address is not valid")
		badRequestError(w, "requested address is missing a required field: "+err.Error())
		return
	}

	addr := models.Address{
		AddressRequest: *addrReq,
		ID:             uuid.NewRandom().String(),
		UserID:         userID,
	}
	rsp := a.db.Create(&addr)
	if rsp.Error != nil {
		log.WithError(rsp.Error).Warnf("Failed to save address %v", addr)
		internalServerError(w, "failed to save address")
		return
	}

	sendJSON(w, http.StatusOK, &struct{ ID string }{ID: addr.ID})
}

// -------------------------------------------------------------------------------------------------------------------
// Helper methods
// -------------------------------------------------------------------------------------------------------------------
func getUser(db *gorm.DB, userID string) *models.User {
	user := &models.User{ID: userID}
	results := db.Find(user)
	if results.RecordNotFound() {
		return nil
	}

	return user
}

func tryDelete(tx *gorm.DB, w http.ResponseWriter, log logrus.FieldLogger, userID string, face interface{}) error {
	typeName := reflect.TypeOf(face).String()

	log.WithField("type", typeName).Debugf("Starting to delete %s", typeName)
	results := tx.Where("user_id = ?", userID).Delete(face)
	if results.Error != nil {
		tx.Rollback()
		log.WithError(results.Error).Warnf("Failed to delete %s", typeName)
		internalServerError(w, "Failed to delete user")
	}

	log.WithField("affected_rows", results.RowsAffected).Debugf("Deleted %d rows", results.RowsAffected)
	return results.Error
}
