package payments

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
)

const (
	catalogStoreID   = "STO_AbCdEfGhIjKlMnOpQrStUv"
	catalogProductID = "PROD_AbCdEfGhIjKlMnOpQrStUv"
)

type catalogRoundTripper func(*http.Request) (*http.Response, error)

func (f catalogRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

var catalogTestKey struct {
	sync.Once
	value string
	err   error
}

func catalogMethod(t *testing.T) Method {
	t.Helper()
	catalogTestKey.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			catalogTestKey.err = err
			return
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			catalogTestKey.err = err
			return
		}
		catalogTestKey.value = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	})
	if catalogTestKey.err != nil {
		t.Fatal(catalogTestKey.err)
	}
	return Method{ID: "waffo-main", Type: "waffo", Name: "Waffo", Enabled: false, Config: map[string]string{
		"merchant_id": "MER_AbCdEfGhIjKlMnOpQrStUv", "private_key": catalogTestKey.value, "environment": "test",
	}}
}

func catalogResponse(status int, payload any) *http.Response {
	data, _ := json.Marshal(payload)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data)))}
}

func catalogProduct(status string) map[string]any {
	return map[string]any{
		"id": catalogProductID, "storeId": catalogStoreID, "name": "Fuel", "description": "Leave some fuel",
		"status": status, "prices": map[string]any{"USD": map[string]any{"amount": "12.34", "taxCategory": "digital_goods"}},
	}
}

func catalogInput() WaffoProductInput {
	return WaffoProductInput{StoreID: catalogStoreID, Name: "Fuel", Description: "Leave some fuel", Currency: "USD", AmountMinor: 1234, TaxCategory: "digital_goods"}
}

func catalogReadBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	var input map[string]any
	if req.Method != "POST" || req.URL.Host != "api.waffo.ai" || req.Header.Get("X-Merchant-Id") == "" || req.Header.Get("X-Signature") == "" {
		t.Fatalf("catalog did not use fixed, signed merchant API: %s %s", req.Method, req.URL)
	}
	if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
		t.Fatal(err)
	}
	if query, ok := input["query"].(string); ok && strings.Contains(query, "onetimeProduct(id: $id)") {
		// The official single-product field takes String!, even though other
		// GraphQL fields use ID!. A permissive JSON mock must not hide this.
		if !strings.Contains(query, "$id: String!") || strings.Contains(query, "$id: ID!") {
			t.Fatalf("single-product GraphQL query must declare $id as String!: %s", query)
		}
	}
	return input
}

func TestWaffoStoresUsesCredentialsWithoutCheckoutConfiguration(t *testing.T) {
	m := catalogMethod(t)
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		if req.URL.Path != "/v1/graphql" || !strings.Contains(input["query"].(string), "prodEnabled") || req.Header.Get("X-Idempotency-Key") != "" {
			t.Fatalf("invalid store query: %v", input)
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"stores": []any{
			map[string]any{"id": catalogStoreID, "name": "My store", "status": "active", "slug": "fuel", "prodEnabled": true},
		}}}), nil
	})}}
	stores, err := s.WaffoStores(context.Background(), m)
	if err != nil || len(stores) != 1 || stores[0].ID != catalogStoreID || !stores[0].ProdEnabled {
		t.Fatalf("stores = %#v, err = %v", stores, err)
	}
}

func TestWaffoProductsPaginatesAndConvertsGraphQLPrices(t *testing.T) {
	m := catalogMethod(t)
	var offsets []int
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		query := input["query"].(string)
		// Live introspection confirms storeId is an onetimeProducts argument,
		// not a field in OnetimeProductFilter. Model that schema restriction so
		// an otherwise permissive JSON fixture cannot accept the old query.
		if !regexp.MustCompile(`onetimeProducts\s*\(\s*storeId\s*:\s*\$storeId\b`).MatchString(query) || regexp.MustCompile(`\bstoreId\s*:\s*\{`).MatchString(query) {
			t.Fatalf("product query must use top-level storeId variable, not filter.storeId: %s", query)
		}
		if !strings.Contains(query, "prices { currency priceInfo { amount taxCategory } }") || strings.Contains(query, "status: { eq:") {
			t.Fatalf("invalid product query: %s", query)
		}
		variables := input["variables"].(map[string]any)
		if variables["storeId"] != catalogStoreID || variables["limit"] != float64(100) {
			t.Fatalf("invalid pagination scope: %v", variables)
		}
		offset := int(variables["offset"].(float64))
		offsets = append(offsets, offset)
		count := 100
		if offset == 100 {
			count = 1
		}
		items := make([]any, count)
		for i := range count {
			items[i] = map[string]any{
				"id": fmt.Sprintf("PROD_%022d", offset+i), "name": "Fuel", "status": "inactive", "hasProdVersion": true,
				"prices": []any{
					map[string]any{"currency": "USD", "priceInfo": map[string]string{"amount": "12.34", "taxCategory": "digital_goods"}},
					map[string]any{"currency": "JPY", "priceInfo": map[string]string{"amount": "1234", "taxCategory": "digital_goods"}},
				},
			}
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"onetimeProducts": items}}), nil
	})}}
	products, err := s.WaffoProducts(context.Background(), m, catalogStoreID)
	if err != nil || len(products) != 101 || len(offsets) != 2 || offsets[1] != 100 {
		t.Fatalf("products count = %d, offsets = %v, err = %v", len(products), offsets, err)
	}
	if products[0].Prices["USD"].AmountMinor != 1234 || products[0].Prices["JPY"].AmountMinor != 1234 || products[0].Status != "inactive" || products[0].HasProdVersion == nil || !*products[0].HasProdVersion {
		t.Fatalf("product conversion = %#v", products[0])
	}
}

func TestWaffoCatalogRejectsPartialAndUnsuccessfulResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   any
	}{
		{"graphql partial errors", 200, map[string]any{"data": map[string]any{"stores": []any{}}, "errors": []any{map[string]string{"message": "Denied"}}}},
		{"missing stores", 200, map[string]any{"data": map[string]any{}}},
		{"null stores", 200, map[string]any{"data": map[string]any{"stores": nil}}},
		{"http error with data", 503, map[string]any{"data": map[string]any{"stores": []any{}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
				return catalogResponse(test.status, test.body), nil
			})}}
			if _, err := s.WaffoStores(context.Background(), catalogMethod(t)); err == nil {
				t.Fatal("unsuccessful provider response was treated as a store list")
			}
		})
	}
}

func TestWaffoCreatePublishRetriesKeepDistinctStageKeys(t *testing.T) {
	m := catalogMethod(t)
	cache := make(map[string]any)
	seenPathKeys := make(map[string][]string)
	createExecutions, publishExecutions, publishAttempts := 0, 0, 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		path := req.URL.Path
		key := req.Header.Get("X-Idempotency-Key")
		if key == "" || len(key) > 256 {
			t.Fatal("catalog mutation missing a valid provider idempotency key")
		}
		seenPathKeys[path] = append(seenPathKeys[path], key)
		if response, hit := cache[key]; hit {
			return catalogResponse(200, response), nil
		}
		switch path {
		case "/v1/actions/onetime-product/create-product":
			createExecutions++
			prices := input["prices"].(map[string]any)
			price := prices["USD"].(map[string]any)
			if input["storeId"] != catalogStoreID || price["amount"] != "12.34" || price["taxCategory"] != "digital_goods" {
				t.Fatalf("wrong product creation payload: %#v", input)
			}
		case "/v1/actions/onetime-product/publish-product":
			publishAttempts++
			if publishAttempts == 1 {
				return catalogResponse(503, map[string]any{"errors": []any{map[string]string{"message": "Try later"}}}), nil
			}
			publishExecutions++
			if input["id"] != catalogProductID {
				t.Fatal("publishing an unexpected product")
			}
		default:
			t.Fatalf("unexpected endpoint: %s", path)
		}
		response := map[string]any{"data": map[string]any{"product": catalogProduct("active")}}
		cache[key] = response
		return catalogResponse(200, response), nil
	})}}
	in := catalogInput()
	in.Publish = true
	if _, err := s.WaffoCreateProduct(context.Background(), m, in, "request-1"); err == nil {
		t.Fatal("failed first publish reported success")
	}
	product, err := s.WaffoCreateProduct(context.Background(), m, in, "request-1")
	if err != nil || product.ID != catalogProductID || product.HasProdVersion == nil || !*product.HasProdVersion {
		t.Fatalf("retry product = %#v, err = %v", product, err)
	}
	createKeys := seenPathKeys["/v1/actions/onetime-product/create-product"]
	publishKeys := seenPathKeys["/v1/actions/onetime-product/publish-product"]
	if createExecutions != 1 || publishExecutions != 1 || len(createKeys) != 2 || len(publishKeys) != 2 || createKeys[0] != createKeys[1] || publishKeys[0] != publishKeys[1] || createKeys[0] == publishKeys[0] {
		t.Fatalf("retries duplicated writes or conflated stages: create=%d publish=%d keys=%v", createExecutions, publishExecutions, seenPathKeys)
	}
}

func TestWaffoCreateLeavesDraftUnlessExplicitlyPublished(t *testing.T) {
	calls := 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Path != "/v1/actions/onetime-product/create-product" {
			t.Fatal("implicitly publishing product")
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"product": catalogProduct("active")}}), nil
	})}}
	product, err := s.WaffoCreateProduct(context.Background(), catalogMethod(t), catalogInput(), "draft-1")
	if err != nil || calls != 1 || product.HasProdVersion != nil {
		t.Fatalf("draft product = %#v, calls = %d, err = %v", product, calls, err)
	}
}

func TestWaffoUpdateClearsDescriptionAndDeleteDeactivates(t *testing.T) {
	m := catalogMethod(t)
	var paths []string
	var keys []string
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		paths = append(paths, req.URL.Path)
		keys = append(keys, req.Header.Get("X-Idempotency-Key"))
		status := "active"
		if input["id"] != catalogProductID {
			t.Fatal("wrong product id")
		}
		switch req.URL.Path {
		case "/v1/actions/onetime-product/update-product":
			if value, exists := input["description"]; !exists || value != "" {
				t.Fatal("empty description was omitted instead of clearing it")
			}
		case "/v1/actions/onetime-product/update-status":
			if input["status"] != "inactive" {
				t.Fatal("delete did not deactivate product")
			}
			status = "inactive"
		default:
			t.Fatalf("invented product endpoint: %s", req.URL.Path)
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"product": catalogProduct(status)}}), nil
	})}}
	in := catalogInput()
	in.Description = ""
	in.Prices = map[string]WaffoPrice{"USD": {AmountMinor: 1234, TaxCategory: "digital_goods"}}
	if _, err := s.WaffoUpdateProduct(context.Background(), m, catalogProductID, in, "same-client-key"); err != nil {
		t.Fatal(err)
	}
	product, err := s.WaffoDeleteProduct(context.Background(), m, catalogProductID, "same-client-key")
	if err != nil || product.Status != "inactive" || len(paths) != 2 || keys[0] == keys[1] {
		t.Fatalf("product = %#v paths = %v keys = %v err = %v", product, paths, keys, err)
	}
}

func TestWaffoUpdateFetchesAndPreservesOtherCurrencyPrices(t *testing.T) {
	m := catalogMethod(t)
	reads, writes := 0, 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		if req.URL.Path == "/v1/graphql" {
			reads++
			if input["variables"].(map[string]any)["id"] != catalogProductID || req.Header.Get("X-Idempotency-Key") != "" {
				t.Fatal("existing price read was not scoped to this product")
			}
			return catalogResponse(200, map[string]any{"data": map[string]any{"onetimeProduct": map[string]any{"id": catalogProductID, "prices": []any{
				map[string]any{"currency": "USD", "priceInfo": map[string]string{"amount": "5.00", "taxCategory": "software"}},
				map[string]any{"currency": "EUR", "priceInfo": map[string]string{"amount": "7.89", "taxCategory": "consulting"}},
			}}}}), nil
		}
		if req.URL.Path != "/v1/actions/onetime-product/update-product" {
			t.Fatalf("unexpected path: %s", req.URL.Path)
		}
		writes++
		prices := input["prices"].(map[string]any)
		usd := prices["USD"].(map[string]any)
		eur := prices["EUR"].(map[string]any)
		if len(prices) != 2 || usd["amount"] != "12.34" || usd["taxCategory"] != "digital_goods" || eur["amount"] != "7.89" || eur["taxCategory"] != "consulting" {
			t.Fatalf("lost existing currency or did not update selected price: %v", prices)
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"product": catalogProduct("active")}}), nil
	})}}
	if _, err := s.WaffoUpdateProduct(context.Background(), m, catalogProductID, catalogInput(), "preserve-prices"); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || writes != 1 {
		t.Fatalf("reads=%d writes=%d", reads, writes)
	}
}

func TestWaffoSuppliedMultiCurrencyPricesMergeWithoutMutatingInput(t *testing.T) {
	in := catalogInput()
	in.Prices = map[string]WaffoPrice{"EUR": {AmountMinor: 789, TaxCategory: "consulting"}, "USD": {AmountMinor: 500, TaxCategory: "software"}}
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		if req.URL.Path != "/v1/actions/onetime-product/update-product" {
			t.Fatal("supplied map unexpectedly read or rewrote another endpoint")
		}
		prices := input["prices"].(map[string]any)
		if len(prices) != 2 || prices["EUR"].(map[string]any)["amount"] != "7.89" || prices["USD"].(map[string]any)["amount"] != "12.34" {
			t.Fatalf("incorrect merged multi-currency prices: %v", prices)
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"product": catalogProduct("active")}}), nil
	})}}
	if _, err := s.WaffoUpdateProduct(context.Background(), catalogMethod(t), catalogProductID, in, "supplied-prices"); err != nil {
		t.Fatal(err)
	}
	if in.Prices["USD"].AmountMinor != 500 {
		t.Fatal("input map changed")
	}
}

func TestWaffoResumeUsesKnownProductAfterCreatePublishFailure(t *testing.T) {
	m := catalogMethod(t)
	in := catalogInput()
	in.Publish = true
	createCount, publishCount := 0, 0
	var publishKeys []string
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		catalogReadBody(t, req)
		switch req.URL.Path {
		case "/v1/graphql":
			return catalogResponse(200, map[string]any{"data": map[string]any{"onetimeProduct": map[string]any{"id": catalogProductID, "hasProdVersion": false}}}), nil
		case "/v1/actions/onetime-product/create-product":
			createCount++
		case "/v1/actions/onetime-product/publish-product":
			publishCount++
			publishKeys = append(publishKeys, req.Header.Get("X-Idempotency-Key"))
			if publishCount == 1 {
				return catalogResponse(503, map[string]any{}), nil
			}
		default:
			t.Fatalf("resume called unexpected endpoint %s", req.URL.Path)
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"product": catalogProduct("active")}}), nil
	})}}
	partial, err := s.WaffoCreateProduct(context.Background(), m, in, "resumable-create")
	if err == nil || partial.ID != catalogProductID {
		t.Fatalf("known product lost after publish failure: %#v err=%v", partial, err)
	}
	complete, err := s.WaffoResumeProductChange(context.Background(), m, partial, in, "create", "resumable-create")
	if err != nil || complete.HasProdVersion == nil || !*complete.HasProdVersion || createCount != 1 || publishCount != 2 || publishKeys[0] != publishKeys[1] {
		t.Fatalf("resume recreated product or changed stage key: product=%#v err=%v create=%d publish=%d keys=%v", complete, err, createCount, publishCount, publishKeys)
	}
}

func TestWaffoResumeReadsPublicationAfterLostPublishResponse(t *testing.T) {
	m := catalogMethod(t)
	in := catalogInput()
	in.Publish = true
	partial := WaffoProduct{ID: catalogProductID, StoreID: catalogStoreID, Name: in.Name, Description: in.Description, Status: "active", Prices: map[string]WaffoPrice{
		"USD": {AmountMinor: 1234, TaxCategory: "digital_goods"}, "EUR": {AmountMinor: 789, TaxCategory: "consulting"},
	}}
	reads := 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		if req.URL.Path != "/v1/graphql" || !strings.Contains(input["query"].(string), "hasProdVersion") || input["variables"].(map[string]any)["id"] != catalogProductID || req.Header.Get("X-Idempotency-Key") != "" {
			t.Fatalf("resume re-created/updated/published instead of reading the known product: %s %v", req.URL.Path, input)
		}
		reads++
		return catalogResponse(200, map[string]any{"data": map[string]any{"onetimeProduct": map[string]any{"id": catalogProductID, "hasProdVersion": true}}}), nil
	})}}
	complete, err := s.WaffoResumeProductChange(context.Background(), m, partial, in, "create", "old-key-after-provider-cache-expiry")
	if err != nil || reads != 1 || complete.ID != catalogProductID || complete.HasProdVersion == nil || !*complete.HasProdVersion || len(complete.Prices) != 2 || complete.Prices["EUR"] != partial.Prices["EUR"] {
		t.Fatalf("publication recovery lost product or prices: product=%#v reads=%d err=%v", complete, reads, err)
	}
}

func TestWaffoResumePublicationReadMustConfirmExactProduct(t *testing.T) {
	for _, product := range []any{
		nil,
		map[string]any{"id": catalogProductID},
		map[string]any{"id": "PROD_0000000000000000000000", "hasProdVersion": true},
	} {
		in := catalogInput()
		in.Publish = true
		partial := WaffoProduct{ID: catalogProductID, StoreID: catalogStoreID, Name: in.Name, Status: "active", Prices: map[string]WaffoPrice{"USD": {AmountMinor: 1234, TaxCategory: "digital_goods"}}}
		calls := 0
		s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
			calls++
			if req.URL.Path != "/v1/graphql" {
				t.Fatal("unconfirmed publication state led to a provider write")
			}
			return catalogResponse(200, map[string]any{"data": map[string]any{"onetimeProduct": product}}), nil
		})}}
		known, err := s.WaffoResumeProductChange(context.Background(), catalogMethod(t), partial, in, "create", "unknown-publication-state")
		if err == nil || known.ID != catalogProductID || calls != 1 {
			t.Fatalf("uncertain publication accepted or known product lost: %#v calls=%d err=%v", known, calls, err)
		}
	}
}

func TestWaffoUpdateDoesNotSaveTestDraftWhenFirstPublishAlreadyExists(t *testing.T) {
	in := catalogInput()
	in.Publish = true
	reads := 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		input := catalogReadBody(t, req)
		if req.URL.Path != "/v1/graphql" || !strings.Contains(input["query"].(string), "hasProdVersion") {
			t.Fatal("already-published first-publish update wrote content")
		}
		reads++
		return catalogResponse(200, map[string]any{"data": map[string]any{"onetimeProduct": map[string]any{"id": catalogProductID, "hasProdVersion": true}}}), nil
	})}}
	product, err := s.WaffoUpdateProduct(context.Background(), catalogMethod(t), catalogProductID, in, "reject-republication")
	if err == nil || product.ID != "" || reads != 1 {
		t.Fatalf("already-published draft incorrectly saved: %#v reads=%d err=%v", product, reads, err)
	}
}

func TestWaffoResumeSkipsCompletedPublishAfterLaterStatusFailure(t *testing.T) {
	m := catalogMethod(t)
	in := catalogInput()
	in.Publish = true
	in.Status = "inactive"
	createCount, publishCount, statusCount := 0, 0, 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		catalogReadBody(t, req)
		status := "active"
		switch req.URL.Path {
		case "/v1/actions/onetime-product/create-product":
			createCount++
		case "/v1/actions/onetime-product/publish-product":
			publishCount++
		case "/v1/actions/onetime-product/update-status":
			statusCount++
			if statusCount == 1 {
				return catalogResponse(503, map[string]any{}), nil
			}
			status = "inactive"
		default:
			t.Fatalf("unexpected resume endpoint: %s", req.URL.Path)
		}
		return catalogResponse(200, map[string]any{"data": map[string]any{"product": catalogProduct(status)}}), nil
	})}}
	partial, err := s.WaffoCreateProduct(context.Background(), m, in, "resume-after-publish")
	if err == nil || partial.ID != catalogProductID || partial.HasProdVersion == nil || !*partial.HasProdVersion {
		t.Fatalf("completed publish lost after status failure: %#v err=%v", partial, err)
	}
	complete, err := s.WaffoResumeProductChange(context.Background(), m, partial, in, "create", "resume-after-publish")
	if err != nil || complete.Status != "inactive" || complete.HasProdVersion == nil || !*complete.HasProdVersion || createCount != 1 || publishCount != 1 || statusCount != 2 {
		t.Fatalf("resume repeated completed write: %#v err=%v create=%d publish=%d status=%d", complete, err, createCount, publishCount, statusCount)
	}
}

func TestWaffoCatalogValidationPreventsInvalidProviderWrites(t *testing.T) {
	m := catalogMethod(t)
	calls := 0
	s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		return catalogResponse(200, nil), nil
	})}}
	for _, mutate := range []func(*WaffoProductInput){
		func(in *WaffoProductInput) { in.StoreID = "STO_invalid" },
		func(in *WaffoProductInput) { in.Name = strings.Repeat("字", 65) },
		func(in *WaffoProductInput) { in.AmountMinor = 99 },
		func(in *WaffoProductInput) { in.AmountMinor = 1_000_001 },
		func(in *WaffoProductInput) { in.Currency = "TWD" },
		func(in *WaffoProductInput) { in.TaxCategory = "donation" },
	} {
		in := catalogInput()
		mutate(&in)
		if _, err := s.WaffoCreateProduct(context.Background(), m, in, "validation-1"); err == nil {
			t.Fatalf("invalid product accepted: %#v", in)
		}
	}
	if _, err := s.WaffoCreateProduct(context.Background(), m, catalogInput(), ""); err == nil {
		t.Fatal("write accepted without idempotency key")
	}
	if calls != 0 {
		t.Fatalf("invalid catalog input made %d provider requests", calls)
	}
}

func TestWaffoCatalogRejectsInvalidReturnedProductPrices(t *testing.T) {
	for _, price := range []map[string]string{
		{"amount": "12.345", "taxCategory": "digital_goods"},
		{"amount": "-1.00", "taxCategory": "digital_goods"},
		{"amount": "12.34", "taxCategory": "donation"},
	} {
		product := catalogProduct("active")
		product["prices"] = map[string]any{"USD": price}
		s := &Service{Client: &http.Client{Transport: catalogRoundTripper(func(req *http.Request) (*http.Response, error) {
			return catalogResponse(200, map[string]any{"data": map[string]any{"product": product}}), nil
		})}}
		if _, err := s.WaffoCreateProduct(context.Background(), catalogMethod(t), catalogInput(), "invalid-response"); err == nil {
			t.Fatalf("invalid provider price accepted: %#v", price)
		}
	}
}

func TestWaffoCurrencyAmountsMatchDocumentedPerCurrencyLimits(t *testing.T) {
	for _, test := range []struct {
		currency string
		min, max int64
	}{
		{"USD", 100, 1_000_000}, {"EUR", 100, 940_000}, {"GBP", 100, 815_000},
		{"HKD", 800, 7_760_000}, {"JPY", 100, 1_600_000}, {"CNY", 100, 100_000},
	} {
		for _, minor := range []int64{test.min, test.max} {
			if err := waffoCurrencyAmount(minor, test.currency); err != nil {
				t.Fatalf("valid %s amount %d rejected: %v", test.currency, minor, err)
			}
		}
		for _, minor := range []int64{test.min - 1, test.max + 1} {
			if err := waffoCurrencyAmount(minor, test.currency); err == nil {
				t.Fatalf("out-of-range %s amount %d accepted", test.currency, minor)
			}
		}
	}
	if err := waffoCurrencyAmount(1000, "TWD"); err == nil {
		t.Fatal("unsupported Waffo TWD accepted")
	}
}
