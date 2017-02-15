package calculator

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type TestItem struct {
	price    uint64
	itemType string
	vat      uint64
	items    []Item
}

func (t *TestItem) PriceInLowestUnit() uint64 {
	return t.price
}

func (t *TestItem) ProductType() string {
	return t.itemType
}

func (t *TestItem) FixedVAT() uint64 {
	return t.vat
}

func (t *TestItem) TaxableItems() []Item {
	return t.items
}

type TestCoupon struct {
	itemType   string
	moreThan   uint64
	percentage uint64
	fixed      uint64
}

func (c *TestCoupon) ValidForType(productType string) bool {
	return c.itemType == productType
}

func (c *TestCoupon) ValidForPrice(currency string, price uint64) bool {
	return c.moreThan == 0 || price > c.moreThan
}

func (c *TestCoupon) PercentageDiscount() uint64 {
	return c.percentage
}

func (c *TestCoupon) FixedDiscount() uint64 {
	return c.fixed
}

func TestNoItems(t *testing.T) {
	price := CalculatePrice(nil, "USA", "USD", nil, nil)
	assert.Equal(t, uint64(0), price.Total)
}

func TestNoTaxes(t *testing.T) {
	price := CalculatePrice(nil, "USA", "USD", nil, []Item{&TestItem{price: 100, itemType: "test"}})

	assert.Equal(t, uint64(100), price.Subtotal)
	assert.Equal(t, uint64(0), price.Taxes)
	assert.Equal(t, uint64(0), price.Discount)
	assert.Equal(t, uint64(100), price.Total)
}

func TestFixedVAT(t *testing.T) {
	price := CalculatePrice(nil, "USA", "USD", nil, []Item{&TestItem{price: 100, itemType: "test", vat: 9}})

	assert.Equal(t, uint64(100), price.Subtotal)
	assert.Equal(t, uint64(9), price.Taxes)
	assert.Equal(t, uint64(0), price.Discount)
	assert.Equal(t, uint64(109), price.Total)
}

func TestFixedVATWhenPricesIncludeTaxes(t *testing.T) {
	price := CalculatePrice(&Settings{PricesIncludeTaxes: true}, "USA", "USD", nil, []Item{&TestItem{price: 100, itemType: "test", vat: 9}})

	assert.Equal(t, uint64(92), price.Subtotal)
	assert.Equal(t, uint64(8), price.Taxes)
	assert.Equal(t, uint64(0), price.Discount)
	assert.Equal(t, uint64(100), price.Total)
}

func TestCountryBasedVAT(t *testing.T) {
	settings := &Settings{
		Taxes: []*Tax{&Tax{
			Percentage:   21,
			ProductTypes: []string{"test"},
			Countries:    []string{"USA"},
		}},
	}

	price := CalculatePrice(settings, "USA", "USD", nil, []Item{&TestItem{price: 100, itemType: "test"}})

	assert.Equal(t, uint64(100), price.Subtotal)
	assert.Equal(t, uint64(21), price.Taxes)
	assert.Equal(t, uint64(0), price.Discount)
	assert.Equal(t, uint64(121), price.Total)
}

func TestCouponWithNoTaxes(t *testing.T) {
	coupon := &TestCoupon{itemType: "test", percentage: 10}
	price := CalculatePrice(nil, "USA", "USD", coupon, []Item{&TestItem{price: 100, itemType: "test"}})

	assert.Equal(t, uint64(100), price.Subtotal)
	assert.Equal(t, uint64(0), price.Taxes)
	assert.Equal(t, uint64(10), price.Discount)
	assert.Equal(t, uint64(90), price.Total)
}

func TestCouponWithNoVAT(t *testing.T) {
	coupon := &TestCoupon{itemType: "test", percentage: 10}
	price := CalculatePrice(nil, "USA", "USD", coupon, []Item{&TestItem{price: 100, itemType: "test", vat: 9}})

	assert.Equal(t, uint64(100), price.Subtotal)
	assert.Equal(t, uint64(9), price.Taxes)
	assert.Equal(t, uint64(10), price.Discount)
	assert.Equal(t, uint64(99), price.Total)
}

func TestCouponWithNoVATWhenPRiceIncludeTaxes(t *testing.T) {
	coupon := &TestCoupon{itemType: "test", percentage: 10}
	settings := &Settings{PricesIncludeTaxes: true}
	price := CalculatePrice(settings, "USA", "USD", coupon, []Item{&TestItem{price: 100, itemType: "test", vat: 9}})

	assert.Equal(t, uint64(92), price.Subtotal)
	assert.Equal(t, uint64(8), price.Taxes)
	assert.Equal(t, uint64(10), price.Discount)
	assert.Equal(t, uint64(90), price.Total)
}

func TestPricingItems(t *testing.T) {
	settings := &Settings{Taxes: []*Tax{&Tax{
		Percentage:   7,
		ProductTypes: []string{"book"},
		Countries:    []string{"DE"},
	}, &Tax{
		Percentage:   21,
		ProductTypes: []string{"ebook"},
		Countries:    []string{"DE"},
	}}}
	item := &TestItem{
		price:    100,
		itemType: "book",
		items: []Item{&TestItem{
			price:    80,
			itemType: "book",
		}, &TestItem{
			price:    20,
			itemType: "ebook",
		}},
	}
	price := CalculatePrice(settings, "DE", "USD", nil, []Item{item})

	assert.Equal(t, uint64(100), price.Subtotal)
	assert.Equal(t, uint64(10), price.Taxes)
	assert.Equal(t, uint64(0), price.Discount)
	assert.Equal(t, uint64(110), price.Total)
}
