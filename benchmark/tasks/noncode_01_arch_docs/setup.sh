#!/bin/bash
set -e
REPO="/tmp/repos/noncode_01_arch_docs"
rm -rf "$REPO"
mkdir -p "$REPO"/{cmd/server,internal/{handler,service,repo,middleware},pkg/validator}
cd "$REPO"

cat > go.mod << 'EOF'
module github.com/example/shop-api
go 1.21
require github.com/golang-jwt/jwt/v5 v5.2.0
require github.com/mattn/go-sqlite3 v1.14.22
EOF

cat > cmd/server/main.go << 'EOF'
package main
import (
	"log"
	"net/http"
	"github.com/example/shop-api/internal/handler"
	"github.com/example/shop-api/internal/middleware"
	"github.com/example/shop-api/internal/repo"
	"github.com/example/shop-api/internal/service"
)
func main() {
	db, err := repo.NewDB("shop.db")
	if err != nil { log.Fatal(err) }
	defer db.Close()
	userRepo := repo.NewUserRepo(db)
	orderRepo := repo.NewOrderRepo(db)
	userSvc := service.NewUserService(userRepo)
	orderSvc := service.NewOrderService(orderRepo, userRepo)
	authMW := middleware.NewAuthMiddleware("jwt-secret-key")
	mux := http.NewServeMux()
	userH := handler.NewUserHandler(userSvc)
	orderH := handler.NewOrderHandler(orderSvc)
	mux.HandleFunc("POST /api/users", userH.Create)
	mux.HandleFunc("GET /api/users/", userH.GetByID)
	mux.HandleFunc("POST /api/orders", orderH.Create)
	mux.HandleFunc("GET /api/orders/", orderH.GetByID)
	handler := authMW.Middleware(mux)
	log.Println("Server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}
EOF

cat > internal/repo/db.go << 'EOF'
package repo
import ("database/sql"; _ "github.com/mattn/go-sqlite3")
type DB struct{ conn *sql.DB }
func NewDB(dsn string) (*DB, error) {
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil { return nil, err }
	return &DB{conn: conn}, conn.Ping()
}
func (d *DB) Close() error { return d.conn.Close() }
func (d *DB) Conn() *sql.DB { return d.conn }

type UserRepo struct{ db *DB }
func NewUserRepo(db *DB) *UserRepo { return &UserRepo{db: db} }
func (r *UserRepo) FindByID(id int64) (*User, error) {
	row := r.db.Conn().QueryRow("SELECT id,username,email,created_at FROM users WHERE id=?", id)
	u := &User{}
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.CreatedAt)
	return u, err
}
func (r *UserRepo) Create(u *User) error {
	_, err := r.db.Conn().Exec("INSERT INTO users(username,email) VALUES(?,?)", u.Username, u.Email)
	return err
}
type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email"`
	CreatedAt string `json:"created_at"`
}

type OrderRepo struct{ db *DB }
func NewOrderRepo(db *DB) *OrderRepo { return &OrderRepo{db: db} }
func (r *OrderRepo) FindByID(id int64) (*Order, error) {
	row := r.db.Conn().QueryRow("SELECT id,user_id,amount,status FROM orders WHERE id=?", id)
	o := &Order{}
	err := row.Scan(&o.ID, &o.UserID, &o.Amount, &o.Status)
	return o, err
}
func (r *OrderRepo) Create(o *Order) error {
	_, err := r.db.Conn().Exec("INSERT INTO orders(user_id,amount,status) VALUES(?,?,?)", o.UserID, o.Amount, o.Status)
	return err
}
type Order struct {
	ID     int64   `json:"id"`
	UserID int64   `json:"user_id"`
	Amount float64 `json:"amount"`
	Status string  `json:"status"`
}
EOF

cat > internal/handler/user.go << 'EOF'
package handler
import ("encoding/json"; "net/http"; "strconv"; "strings"; "github.com/example/shop-api/internal/service")
type UserHandler struct{ svc *service.UserService }
func NewUserHandler(svc *service.UserService) *UserHandler { return &UserHandler{svc: svc} }
func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username string `json:"username"`; Email string `json:"email"` }
	json.NewDecoder(r.Body).Decode(&req)
	user, err := h.svc.CreateUser(req.Username, req.Email)
	if err != nil { http.Error(w, err.Error(), 500); return }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
func (h *UserHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/users/")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	user, err := h.svc.GetUser(id)
	if err != nil { http.Error(w, "not found", 404); return }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}
EOF

cat > internal/handler/order.go << 'EOF'
package handler
import ("encoding/json"; "net/http"; "strconv"; "strings"; "github.com/example/shop-api/internal/service")
type OrderHandler struct{ svc *service.OrderService }
func NewOrderHandler(svc *service.OrderService) *OrderHandler { return &OrderHandler{svc: svc} }
func (h *OrderHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct{ UserID int64 `json:"user_id"`; Amount float64 `json:"amount"` }
	json.NewDecoder(r.Body).Decode(&req)
	order, err := h.svc.CreateOrder(req.UserID, req.Amount)
	if err != nil { http.Error(w, err.Error(), 500); return }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(order)
}
func (h *OrderHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	idStr := strings.TrimPrefix(r.URL.Path, "/api/orders/")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	order, err := h.svc.GetOrder(id)
	if err != nil { http.Error(w, "not found", 404); return }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(order)
}
EOF

cat > internal/service/user.go << 'EOF'
package service
import ("errors"; "github.com/example/shop-api/internal/repo"; "github.com/example/shop-api/pkg/validator")
type UserService struct{ repo *repo.UserRepo }
func NewUserService(repo *repo.UserRepo) *UserService { return &UserService{repo: repo} }
func (s *UserService) CreateUser(username, email string) (*repo.User, error) {
	if !validator.IsValidEmail(email) { return nil, errors.New("invalid email") }
	u := &repo.User{Username: username, Email: email}
	return u, s.repo.Create(u)
}
func (s *UserService) GetUser(id int64) (*repo.User, error) { return s.repo.FindByID(id) }
EOF

cat > internal/service/order.go << 'EOF'
package service
import ("errors"; "github.com/example/shop-api/internal/repo")
type OrderService struct{ orderRepo *repo.OrderRepo; userRepo *repo.UserRepo }
func NewOrderService(orderRepo *repo.OrderRepo, userRepo *repo.UserRepo) *OrderService {
	return &OrderService{orderRepo: orderRepo, userRepo: userRepo}
}
func (s *OrderService) CreateOrder(userID int64, amount float64) (*repo.Order, error) {
	if _, err := s.userRepo.FindByID(userID); err != nil { return nil, errors.New("user not found") }
	if amount <= 0 { return nil, errors.New("amount must be positive") }
	o := &repo.Order{UserID: userID, Amount: amount, Status: "pending"}
	return o, s.orderRepo.Create(o)
}
func (s *OrderService) GetOrder(id int64) (*repo.Order, error) { return s.orderRepo.FindByID(id) }
EOF

cat > internal/middleware/auth.go << 'EOF'
package middleware
import ("net/http"; "strings")
type AuthMiddleware struct{ secret string }
func NewAuthMiddleware(secret string) *AuthMiddleware { return &AuthMiddleware{secret: secret} }
func (m *AuthMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "unauthorized", 401)
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if !m.validateToken(token) {
			http.Error(w, "invalid token", 401)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (m *AuthMiddleware) validateToken(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 { return false }
	return true
}
EOF

cat > pkg/validator/email.go << 'EOF'
package validator
import "regexp"
var emailRegex = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)
func IsValidEmail(email string) bool { return emailRegex.MatchString(email) }
EOF

cat > README.md << 'EOF'
# Shop API

A simple e-commerce API built with Go's standard library and SQLite.

## Architecture
- HTTP handlers (cmd/server + internal/handler)
- Business logic (internal/service)
- Data access (internal/repo)
- Cross-cutting concerns (internal/middleware)
- Utilities (pkg/validator)

## Running
```bash
go run cmd/server/main.go
```
EOF

echo "noncode_01_arch_docs setup done"
