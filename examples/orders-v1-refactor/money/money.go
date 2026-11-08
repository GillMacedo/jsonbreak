// Package money holds an amount type that encodes itself as a string.
package money

import (
	"encoding/json"
	"fmt"
)

// Cents is an amount in cents, encoded as a decimal string such as "12.50".
type Cents int64

// MarshalJSON writes the amount as a decimal string.
func (c Cents) MarshalJSON() ([]byte, error) {
	return json.Marshal(fmt.Sprintf("%d.%02d", c/100, c%100))
}
