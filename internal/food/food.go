// internal/food/food.go
package food

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

// GenerateFoodOrderID returns "L-12345" where L is the (uppercased) first letter of the school.
func GenerateFoodOrderID(school string) (string, error) {
	letter := "X"
	school = strings.TrimSpace(school)
	if len(school) > 0 {
		letter = strings.ToUpper(string(school[0]))
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100000)) // 0-99999
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%05d", letter, n.Int64()), nil
}
