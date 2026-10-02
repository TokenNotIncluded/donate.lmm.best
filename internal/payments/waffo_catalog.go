package payments

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

// WaffoStore is the small merchant-scoped store view used by the admin picker.
type WaffoStore struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Status      string `json:"status"`
	ProdEnabled bool   `json:"prod_enabled"`
}

type WaffoPrice struct {
	AmountMinor int64  `json:"amount_minor"`
	TaxCategory string `json:"tax_category"`
}

type WaffoProduct struct {
	ID             string                `json:"id"`
	StoreID        string                `json:"store_id"`
	Name           string                `json:"name"`
	Description    string                `json:"description"`
	Status         string                `json:"status"`
	Prices         map[string]WaffoPrice `json:"prices"`
	HasProdVersion *bool                 `json:"has_prod_version,omitempty"`
}

// WaffoProductInput edits content and a currency's base price. Prices can carry
// the full existing map; when omitted on update it is read from Waffo first.
// Checkout overrides this base price with the donor's amount. Publish is an
// explicit first publish. Status is optional and accepts active/inactive.
type WaffoProductInput struct {
	StoreID     string                `json:"store_id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Currency    string                `json:"currency"`
	AmountMinor int64                 `json:"amount_minor"`
	TaxCategory string                `json:"tax_category"`
	Publish     bool                  `json:"publish"`
	Status      string                `json:"status,omitempty"`
	Prices      map[string]WaffoPrice `json:"prices,omitempty"`
}

// WaffoStores accepts a disabled draft payment method. Catalog access requires
// its merchant credentials, but not a selected store or product.
func (s *Service) WaffoStores(ctx context.Context, m Method) ([]WaffoStore, error) {
	client, err := s.waffoClient(m)
	if err != nil {
		return nil, err
	}
	raw, err := client.GraphQL.Query(ctx, pancake.GraphQLParams{
		Query: `query { stores { id name slug status prodEnabled } }`,
	})
	if err != nil {
		return nil, err
	}
	var data struct {
		Stores *[]struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Slug        string `json:"slug"`
			Status      string `json:"status"`
			ProdEnabled bool   `json:"prodEnabled"`
		} `json:"stores"`
	}
	if err := decodeWaffoCatalogQuery(raw, &data); err != nil {
		return nil, err
	}
	if data.Stores == nil {
		return nil, errors.New("Waffo did not return a store list")
	}
	stores := make([]WaffoStore, 0, len(*data.Stores))
	for _, store := range *data.Stores {
		if !validWaffoID(store.ID, "STO") || store.Name == "" {
			return nil, errors.New("Waffo returned an invalid store")
		}
		stores = append(stores, WaffoStore{ID: store.ID, Name: store.Name, Slug: store.Slug, Status: store.Status, ProdEnabled: store.ProdEnabled})
	}
	return stores, nil
}

type waffoCatalogGraphProduct struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	HasProdVersion *bool  `json:"hasProdVersion"`
	Prices         []struct {
		Currency  string            `json:"currency"`
		PriceInfo pancake.PriceInfo `json:"priceInfo"`
	} `json:"prices"`
}

// WaffoProducts includes active and inactive products and paginates explicitly;
// the API's default page is too small for a reliable selection list.
func (s *Service) WaffoProducts(ctx context.Context, m Method, storeID string) ([]WaffoProduct, error) {
	if storeID == "" {
		storeID = m.Config["store_id"]
	}
	if !validWaffoID(storeID, "STO") {
		return nil, errors.New("select a valid Waffo store")
	}
	client, err := s.waffoClient(m)
	if err != nil {
		return nil, err
	}
	const pageSize = 100
	products := make([]WaffoProduct, 0)
	seen := make(map[string]bool)
	for offset := 0; offset < 10_000; offset += pageSize {
		raw, err := client.GraphQL.Query(ctx, pancake.GraphQLParams{
			Query: `query ($storeId: String!, $limit: Int!, $offset: Int!) {
				onetimeProducts(storeId: $storeId, limit: $limit, offset: $offset) {
					id name description status hasProdVersion
					prices { currency priceInfo { amount taxCategory } }
				}
			}`,
			Variables: map[string]any{"storeId": storeID, "limit": pageSize, "offset": offset},
		})
		if err != nil {
			return nil, err
		}
		var data struct {
			Products *[]waffoCatalogGraphProduct `json:"onetimeProducts"`
		}
		if err := decodeWaffoCatalogQuery(raw, &data); err != nil {
			return nil, err
		}
		if data.Products == nil || len(*data.Products) > pageSize {
			return nil, errors.New("Waffo did not return a valid product list")
		}
		for _, product := range *data.Products {
			if seen[product.ID] {
				return nil, errors.New("Waffo returned duplicate products while paginating")
			}
			seen[product.ID] = true
			prices := make(pancake.Prices, len(product.Prices))
			for _, price := range product.Prices {
				if _, duplicate := prices[price.Currency]; duplicate {
					return nil, errors.New("Waffo returned duplicate product currencies")
				}
				prices[price.Currency] = price.PriceInfo
			}
			mapped, err := mapWaffoCatalogProduct(pancake.OnetimeProductDetail{
				ID: product.ID, StoreID: storeID, Name: product.Name, Description: &product.Description,
				Status: pancake.ProductVersionStatus(product.Status), Prices: prices,
			})
			if err != nil {
				return nil, err
			}
			mapped.HasProdVersion = product.HasProdVersion
			products = append(products, mapped)
		}
		if len(*data.Products) < pageSize {
			return products, nil
		}
	}
	return nil, errors.New("Waffo catalog exceeds 10000 products; use the Waffo dashboard")
}

func decodeWaffoCatalogQuery(raw *pancake.GraphQLResponse, output any) error {
	if raw == nil {
		return errors.New("Waffo returned an empty catalog response")
	}
	if len(raw.Errors) > 0 {
		return &pancake.Error{Status: 400, Errors: raw.Errors}
	}
	if len(raw.Data) == 0 || string(raw.Data) == "null" {
		return errors.New("Waffo returned empty catalog data")
	}
	if err := json.Unmarshal(raw.Data, output); err != nil {
		return errors.New("Waffo returned invalid catalog data")
	}
	return nil
}

func validateWaffoProductInput(in WaffoProductInput) error {
	if strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 64 || !utf8.ValidString(in.Name) {
		return errors.New("Waffo product name must contain 1 to 64 characters")
	}
	if len(in.Description) > 16_384 || !utf8.ValidString(in.Description) {
		return errors.New("Waffo product description is too long or invalid")
	}
	if err := waffoCurrencyAmount(in.AmountMinor, in.Currency); err != nil {
		return err
	}
	if !validTaxCategory(in.TaxCategory) {
		return errors.New("select the Waffo product's approved tax category")
	}
	if in.Status != "" && in.Status != "active" && in.Status != "inactive" {
		return errors.New("Waffo product status must be active or inactive")
	}
	for currency, price := range in.Prices {
		if currency == in.Currency {
			continue // The explicit amount/tax fields replace this entry.
		}
		if err := waffoCurrencyAmount(price.AmountMinor, currency); err != nil || !validTaxCategory(price.TaxCategory) {
			return errors.New("invalid Waffo multi-currency product price")
		}
	}
	return nil
}

// waffoCurrencyAmount applies the documented Waffo payment boundaries to both
// base catalog prices and the dynamically overridden donation checkout price.
func waffoCurrencyAmount(minor int64, currency string) error {
	// TWD is supported by the site, but is not a documented Waffo currency.
	ranges := map[string][2]int64{
		"USD": {100, 1_000_000}, "EUR": {100, 940_000}, "GBP": {100, 815_000},
		"HKD": {800, 7_760_000}, "JPY": {100, 1_600_000}, "CNY": {100, 100_000},
	}
	limits, supported := ranges[currency]
	if !supported || minor <= 0 || minor > maxAmountMinor {
		return errors.New("invalid Waffo currency or amount (supported: USD, EUR, GBP, HKD, JPY, CNY)")
	}
	if minor < limits[0] || minor > limits[1] {
		return fmt.Errorf("Waffo %s amount must be between %s and %s", currency, amountString(limits[0], currency), amountString(limits[1], currency))
	}
	return nil
}

func waffoCatalogKey(m Method, action, id, key string) (string, error) {
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return "", errors.New("a valid Idempotency-Key is required for Waffo product changes")
	}
	// Key identity includes credentials' merchant scope and action, so create and
	// publish in a single logical operation cannot replay one another's response.
	hash := sha256.Sum256([]byte(m.Config["merchant_id"] + "\x00" + m.Config["environment"] + "\x00" + m.ID + "\x00" + action + "\x00" + id + "\x00" + key))
	return "donate-catalog-" + hex.EncodeToString(hash[:]), nil
}

func productInputPrices(in WaffoProductInput) pancake.Prices {
	prices := make(pancake.Prices, len(in.Prices)+1)
	for currency, price := range in.Prices {
		prices[currency] = pancake.PriceInfo{Amount: amountString(price.AmountMinor, currency), TaxCategory: pancake.TaxCategory(price.TaxCategory)}
	}
	prices[in.Currency] = pancake.PriceInfo{Amount: amountString(in.AmountMinor, in.Currency), TaxCategory: pancake.TaxCategory(in.TaxCategory)}
	return prices
}

func readWaffoProductPrices(ctx context.Context, client *pancake.Client, id string) (pancake.Prices, error) {
	raw, err := client.GraphQL.Query(ctx, pancake.GraphQLParams{
		Query:     `query ($id: String!) { onetimeProduct(id: $id) { id prices { currency priceInfo { amount taxCategory } } } }`,
		Variables: map[string]any{"id": id},
	})
	if err != nil {
		return nil, err
	}
	var data struct {
		Product *waffoCatalogGraphProduct `json:"onetimeProduct"`
	}
	if err := decodeWaffoCatalogQuery(raw, &data); err != nil {
		return nil, err
	}
	if data.Product == nil || data.Product.ID != id || len(data.Product.Prices) == 0 {
		return nil, errors.New("Waffo did not return the product's existing prices")
	}
	prices := make(pancake.Prices, len(data.Product.Prices))
	for _, price := range data.Product.Prices {
		if _, duplicate := prices[price.Currency]; duplicate {
			return nil, errors.New("Waffo returned duplicate product currencies")
		}
		if _, err := parseAmount(price.PriceInfo.Amount, price.Currency); err != nil || !validTaxCategory(string(price.PriceInfo.TaxCategory)) {
			return nil, errors.New("Waffo returned an invalid existing product price")
		}
		prices[price.Currency] = price.PriceInfo
	}
	return prices, nil
}

func (s *Service) WaffoCreateProduct(ctx context.Context, m Method, in WaffoProductInput, key string) (WaffoProduct, error) {
	if in.StoreID == "" {
		in.StoreID = m.Config["store_id"]
	}
	if !validWaffoID(in.StoreID, "STO") {
		return WaffoProduct{}, errors.New("select a valid Waffo store")
	}
	if err := validateWaffoProductInput(in); err != nil {
		return WaffoProduct{}, err
	}
	idempotency, err := waffoCatalogKey(m, "create", in.StoreID, key)
	if err != nil {
		return WaffoProduct{}, err
	}
	client, err := s.waffoClient(m)
	if err != nil {
		return WaffoProduct{}, err
	}
	result, err := client.OnetimeProducts.Create(ctx, pancake.CreateOnetimeProductParams{
		StoreID: in.StoreID, Name: in.Name, Description: &in.Description, Prices: productInputPrices(in),
	}, pancake.WithIdempotencyKey(idempotency))
	if err != nil {
		return WaffoProduct{}, err
	}
	if result == nil || result.Product.StoreID != in.StoreID {
		return WaffoProduct{}, errors.New("Waffo returned a product for an unexpected store")
	}
	return finishWaffoProductChange(ctx, client, m, result.Product, in, "create", key, false)
}

func (s *Service) WaffoUpdateProduct(ctx context.Context, m Method, id string, in WaffoProductInput, key string) (WaffoProduct, error) {
	if !validWaffoID(id, "PROD") {
		return WaffoProduct{}, errors.New("select a valid Waffo product")
	}
	if err := validateWaffoProductInput(in); err != nil {
		return WaffoProduct{}, err
	}
	idempotency, err := waffoCatalogKey(m, "update", id, key)
	if err != nil {
		return WaffoProduct{}, err
	}
	client, err := s.waffoClient(m)
	if err != nil {
		return WaffoProduct{}, err
	}
	if in.Publish {
		published, err := readWaffoProductPublication(ctx, client, id)
		if err != nil {
			return WaffoProduct{}, err
		}
		if published {
			return WaffoProduct{}, errors.New("Waffo product is already published; edit with production credentials and publish:false")
		}
	}
	prices := productInputPrices(in)
	if in.Prices == nil {
		existing, err := readWaffoProductPrices(ctx, client, id)
		if err != nil {
			return WaffoProduct{}, err
		}
		for currency, price := range prices {
			existing[currency] = price
		}
		prices = existing
	}
	result, err := client.OnetimeProducts.Update(ctx, pancake.UpdateOnetimeProductParams{
		ID: id, Name: &in.Name, Description: &in.Description, Prices: prices,
	}, pancake.WithIdempotencyKey(idempotency))
	if err != nil {
		return WaffoProduct{}, err
	}
	if result == nil || result.Product.ID != id || (in.StoreID != "" && result.Product.StoreID != in.StoreID) {
		return WaffoProduct{}, errors.New("Waffo returned an unexpected product")
	}
	return finishWaffoProductChange(ctx, client, m, result.Product, in, "update", key, false)
}

// WaffoResumeProductChange finishes an operation whose content was already
// saved. The application persists the nonempty product returned with an error;
// this path never re-creates or re-updates it, even after provider key expiry.
func (s *Service) WaffoResumeProductChange(ctx context.Context, m Method, partial WaffoProduct, in WaffoProductInput, operation, key string) (WaffoProduct, error) {
	if operation != "create" && operation != "update" {
		return WaffoProduct{}, errors.New("invalid Waffo resume operation")
	}
	if _, err := waffoCatalogKey(m, operation, partial.ID, key); err != nil {
		return WaffoProduct{}, err
	}
	if err := validateWaffoProductInput(in); err != nil {
		return WaffoProduct{}, err
	}
	if in.StoreID != "" && partial.StoreID != in.StoreID {
		return WaffoProduct{}, errors.New("Waffo resume store does not match saved product")
	}
	prices := make(pancake.Prices, len(partial.Prices))
	for currency, price := range partial.Prices {
		prices[currency] = pancake.PriceInfo{Amount: amountString(price.AmountMinor, currency), TaxCategory: pancake.TaxCategory(price.TaxCategory)}
	}
	product := pancake.OnetimeProductDetail{ID: partial.ID, StoreID: partial.StoreID, Name: partial.Name, Description: &partial.Description, Status: pancake.ProductVersionStatus(partial.Status), Prices: prices}
	if _, err := mapWaffoCatalogProduct(product); err != nil {
		return WaffoProduct{}, err
	}
	client, err := s.waffoClient(m)
	if err != nil {
		return partial, err
	}
	published := partial.HasProdVersion != nil && *partial.HasProdVersion
	if in.Publish && !published {
		// The publish may have succeeded while its response was lost. Read the
		// durable provider fact before replaying a first-publish-only command,
		// including after Waffo's 24-hour idempotency cache has expired.
		published, err = readWaffoProductPublication(ctx, client, partial.ID)
		if err != nil {
			return partial, err
		}
	}
	return finishWaffoProductChange(ctx, client, m, product, in, operation, key, published)
}

func readWaffoProductPublication(ctx context.Context, client *pancake.Client, id string) (bool, error) {
	raw, err := client.GraphQL.Query(ctx, pancake.GraphQLParams{
		Query:     `query ($id: String!) { onetimeProduct(id: $id) { id hasProdVersion } }`,
		Variables: map[string]any{"id": id},
	})
	if err != nil {
		return false, err
	}
	var data struct {
		Product *struct {
			ID             string `json:"id"`
			HasProdVersion *bool  `json:"hasProdVersion"`
		} `json:"onetimeProduct"`
	}
	if err := decodeWaffoCatalogQuery(raw, &data); err != nil {
		return false, err
	}
	if data.Product == nil || data.Product.ID != id || data.Product.HasProdVersion == nil {
		return false, errors.New("Waffo did not confirm the saved product's publication state")
	}
	return *data.Product.HasProdVersion, nil
}

func finishWaffoProductChange(ctx context.Context, client *pancake.Client, m Method, product pancake.OnetimeProductDetail, in WaffoProductInput, action, key string, published bool) (WaffoProduct, error) {
	mapped, err := mapWaffoCatalogProduct(product)
	if err != nil {
		return WaffoProduct{}, err
	}
	if published {
		mapped.HasProdVersion = &published
	}
	if in.Status == "active" && product.Status != "active" {
		product, err = setWaffoProductStatus(ctx, client, m, product.ID, "active", action, key)
		if err != nil {
			return mapped, err
		}
		next, err := mapWaffoCatalogProduct(product)
		if err != nil {
			return mapped, err
		}
		mapped = next
		if published {
			mapped.HasProdVersion = &published
		}
	}
	if in.Publish && !published {
		idempotency, _ := waffoCatalogKey(m, action+"-publish", product.ID, key)
		result, err := client.OnetimeProducts.Publish(ctx, pancake.PublishOnetimeProductParams{ID: product.ID}, pancake.WithIdempotencyKey(idempotency))
		if err != nil {
			return mapped, fmt.Errorf("Waffo product saved; first publish failed (retry with the same Idempotency-Key): %w", err)
		}
		if result == nil || result.Product.ID != product.ID {
			return mapped, errors.New("Waffo returned an unexpected published product")
		}
		product = result.Product
		next, err := mapWaffoCatalogProduct(product)
		if err != nil {
			return mapped, err
		}
		mapped = next
		published = true
		mapped.HasProdVersion = &published
	}
	if in.Status == "inactive" && product.Status != "inactive" {
		product, err = setWaffoProductStatus(ctx, client, m, product.ID, "inactive", action, key)
		if err != nil {
			return mapped, err
		}
		next, err := mapWaffoCatalogProduct(product)
		if err != nil {
			return mapped, err
		}
		mapped = next
		if published {
			mapped.HasProdVersion = &published
		}
	}
	return mapped, nil
}

// WaffoDeleteProduct deactivates the product in the merchant key's environment.
// Waffo exposes no product hard-delete operation; existing orders are preserved.
func (s *Service) WaffoDeleteProduct(ctx context.Context, m Method, id, key string) (WaffoProduct, error) {
	if !validWaffoID(id, "PROD") {
		return WaffoProduct{}, errors.New("select a valid Waffo product")
	}
	if _, err := waffoCatalogKey(m, "delete-status-inactive", id, key); err != nil {
		return WaffoProduct{}, err
	}
	client, err := s.waffoClient(m)
	if err != nil {
		return WaffoProduct{}, err
	}
	product, err := setWaffoProductStatus(ctx, client, m, id, "inactive", "delete", key)
	if err != nil {
		return WaffoProduct{}, err
	}
	return mapWaffoCatalogProduct(product)
}

func setWaffoProductStatus(ctx context.Context, client *pancake.Client, m Method, id, status, action, key string) (pancake.OnetimeProductDetail, error) {
	idempotency, err := waffoCatalogKey(m, action+"-status-"+status, id, key)
	if err != nil {
		return pancake.OnetimeProductDetail{}, err
	}
	result, err := client.OnetimeProducts.UpdateStatus(ctx, pancake.UpdateOnetimeStatusParams{
		ID: id, Status: pancake.ProductVersionStatus(status),
	}, pancake.WithIdempotencyKey(idempotency))
	if err != nil {
		return pancake.OnetimeProductDetail{}, err
	}
	if result == nil || result.Product.ID != id || string(result.Product.Status) != status {
		return pancake.OnetimeProductDetail{}, errors.New("Waffo did not confirm the requested product status")
	}
	return result.Product, nil
}

func mapWaffoCatalogProduct(product pancake.OnetimeProductDetail) (WaffoProduct, error) {
	if !validWaffoID(product.ID, "PROD") || !validWaffoID(product.StoreID, "STO") || product.Name == "" || (product.Status != "active" && product.Status != "inactive") || len(product.Prices) == 0 {
		return WaffoProduct{}, errors.New("Waffo returned an invalid product")
	}
	mapped := WaffoProduct{ID: product.ID, StoreID: product.StoreID, Name: product.Name, Status: string(product.Status), Prices: make(map[string]WaffoPrice, len(product.Prices))}
	if product.Description != nil {
		mapped.Description = *product.Description
	}
	for currency, price := range product.Prices {
		amount, err := parseAmount(price.Amount, currency)
		if err != nil || !validTaxCategory(string(price.TaxCategory)) {
			return WaffoProduct{}, errors.New("Waffo returned an invalid product price")
		}
		mapped.Prices[currency] = WaffoPrice{AmountMinor: amount, TaxCategory: string(price.TaxCategory)}
	}
	return mapped, nil
}
