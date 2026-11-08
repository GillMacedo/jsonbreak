// Package api holds the JSON types of a made-up order service.
//
// This copy is a refactor of orders-v1 that keeps the wire format: Go fields
// are renamed and reordered, a field moves into an embedded struct, a named
// type replaces an anonymous one, and new keys are added.
package api

import (
	"time"

	"example.com/orders/money"
)

// Meta is shared by every resource.
type Meta struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Version   int       `json:"version"`
}

// Ownership groups the fields that say who an order belongs to.
type Ownership struct {
	Customer string `json:"customer_id"`
}

// Shipping says how an order ships.
type Shipping struct {
	Method string `json:"method"`
	Days   int32  `json:"days"`
}

// Order is returned by GET /orders/{id}.
type Order struct {
	Meta
	Ownership
	State    string            `json:"status"`
	Lines    []Item            `json:"items"`
	Total    money.Cents       `json:"total"`
	Qty      int               `json:"quantity"`
	Note     string            `json:"note"`
	Ship     Shipping          `json:"shipping"`
	Labels   map[string]string `json:"tags"`
	Currency string            `json:"currency,omitempty"`
}

// Item is one line of an order.
type Item struct {
	SKU   string  `json:"sku"`
	Count int64   `json:"count"`
	Price float32 `json:"price"`
}

// Customer is returned by GET /customers/{id}.
type Customer struct {
	Meta
	Name  string `json:"name"`
	Email string `json:"email"`
}
