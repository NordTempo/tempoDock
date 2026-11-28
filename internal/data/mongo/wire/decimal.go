package wire

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

// Decimal128 text, as the BSON spec writes and reads it: plain notation
// ("249.00") where the exponent is small, else scientific ("1.5E+40"),
// and "Infinity", "-Infinity", "NaN".

const (
	decBias   = 6176
	decMaxExp = 6111
	decMinExp = -6176
	decDigits = 34
)

var decMaxCoef = new(big.Int).Sub(new(big.Int).Exp(big.NewInt(10), big.NewInt(decDigits), nil), big.NewInt(1))

// String writes d as the BSON spec's text form.
func (d Decimal128) String() string {
	neg := d.H>>63 == 1
	sign := ""
	if neg {
		sign = "-"
	}
	var exp int
	coef := new(big.Int)
	switch {
	case (d.H>>58)&0x1F == 0x1F:
		return "NaN"
	case (d.H>>58)&0x1F == 0x1E:
		return sign + "Infinity"
	case (d.H>>61)&3 == 3:
		// A coefficient past 10^34 - 1: non-canonical, read as zero.
		exp = int((d.H>>47)&0x3FFF) - decBias
	case d.H&(1<<49-1) == 0:
		// The common case: a coefficient that fits 64 bits, no big.Int.
		exp = int((d.H>>49)&0x3FFF) - decBias
		return decimalText(sign, strconv.FormatUint(d.L, 10), exp)
	default:
		exp = int((d.H>>49)&0x3FFF) - decBias
		coef.SetUint64(d.H & (1<<49 - 1))
		coef.Lsh(coef, 64)
		coef.Or(coef, new(big.Int).SetUint64(d.L))
		if coef.Cmp(decMaxCoef) > 0 {
			coef.SetInt64(0)
		}
	}
	return decimalText(sign, coef.String(), exp)
}

// ParseDecimal128 reads a decimal: digits with an optional point and
// exponent, or Infinity and NaN. More than 34 significant digits, or an
// exponent out of range, is an error rather than a rounded value.
func ParseDecimal128(s string) (Decimal128, error) {
	bad := errors.New("not a decimal: " + strconv.Quote(s))
	neg := false
	switch {
	case strings.HasPrefix(s, "-"):
		neg, s = true, s[1:]
	case strings.HasPrefix(s, "+"):
		s = s[1:]
	}
	var d Decimal128
	switch strings.ToLower(s) {
	case "nan":
		return Decimal128{H: 0x7C00000000000000}, nil
	case "inf", "infinity":
		d.H = 0x7800000000000000
		if neg {
			d.H |= 1 << 63
		}
		return d, nil
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return d, bad
		}
		exp, s = e, s[:i]
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		exp -= len(s) - i - 1
		s = s[:i] + s[i+1:]
	}
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return d, bad
	}
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	if len(s) > decDigits {
		return d, errors.New("a decimal holds at most 34 digits: " + strconv.Quote(s))
	}
	if exp > decMaxExp || exp < decMinExp {
		return d, errors.New("a decimal's exponent is out of range: " + strconv.Quote(s))
	}
	coef, _ := new(big.Int).SetString(s, 10)
	lo := new(big.Int).And(coef, new(big.Int).SetUint64(^uint64(0)))
	hi := new(big.Int).Rsh(coef, 64)
	d.L = lo.Uint64()
	d.H = hi.Uint64() | uint64(exp+decBias)<<49
	if neg {
		d.H |= 1 << 63
	}
	return d, nil
}

// decimalText is a coefficient's digits with its exponent, as the BSON
// spec writes them.
func decimalText(sign, digits string, exp int) string {
	adjusted := exp + len(digits) - 1
	if exp > 0 || adjusted < -6 {
		s := digits[:1]
		if len(digits) > 1 {
			s += "." + digits[1:]
		}
		e := "E+"
		if adjusted < 0 {
			e = "E"
		}
		return sign + s + e + strconv.Itoa(adjusted)
	}
	if exp == 0 {
		return sign + digits
	}
	point := len(digits) + exp
	if point > 0 {
		return sign + digits[:point] + "." + digits[point:]
	}
	return sign + "0." + strings.Repeat("0", -point) + digits
}
