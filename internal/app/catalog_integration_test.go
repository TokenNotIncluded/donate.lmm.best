package app

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

const appStoreID = "STO_AbCdEfGhIjKlMnOpQrStUv"
const appProductID = "PROD_AbCdEfGhIjKlMnOpQrStUv"

var appCatalogKey struct {
	sync.Once
	key string
	err error
}

func appWaffoMethod(t *testing.T) payments.Method {
	t.Helper()
	appCatalogKey.Do(func() {
		private, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			appCatalogKey.err = err
			return
		}
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			appCatalogKey.err = err
			return
		}
		appCatalogKey.key = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	})
	if appCatalogKey.err != nil {
		t.Fatal(appCatalogKey.err)
	}
	return payments.Method{ID: "waffo", Type: "waffo", Name: "Waffo", Config: map[string]string{"merchant_id": "MER_AbCdEfGhIjKlMnOpQrStUv", "environment": "test", "private_key": appCatalogKey.key, "store_id": appStoreID}}
}

func catalogInputBody() map[string]any {
	return map[string]any{"method_id": "waffo", "store_id": appStoreID, "name": "Fuel", "description": "Keep the project going", "currency": "USD", "amount_minor": 1234, "tax_category": "digital_goods", "prices": map[string]any{"USD": map[string]any{"amount_minor": 1234, "tax_category": "digital_goods"}}}
}

func catalogProviderProduct(status string) map[string]any {
	return map[string]any{"id": appProductID, "storeId": appStoreID, "name": "Fuel", "description": "Keep the project going", "status": status, "prices": map[string]any{"USD": map[string]string{"amount": "12.34", "taxCategory": "digital_goods"}}}
}

func catalogHandler(a *App) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/waffo/products", a.waffoMutate)
	mux.HandleFunc("PUT /api/admin/waffo/products/{id}", a.waffoMutate)
	mux.HandleFunc("DELETE /api/admin/waffo/products/{id}", a.waffoMutate)
	return mux
}

func reopenApp(t *testing.T, a *App) *App {
	t.Helper()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(a.DataDir, a.PublicURL, a.assets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	return next
}

func TestWaffoCatalogCRUDIdempotencySurvivesRestartAndRejectsChangedRequest(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, appWaffoMethod(t))
	var mutationPaths, providerKeys []string
	client := &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Host != "api.waffo.ai" || req.Header.Get("X-Merchant-Id") == "" || req.Header.Get("X-Signature") == "" || req.Header.Get("X-Idempotency-Key") == "" {
			t.Fatalf("catalog mutation did not use signed fixed provider API: %s", req.URL)
		}
		var input map[string]any
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		mutationPaths = append(mutationPaths, req.URL.Path)
		providerKeys = append(providerKeys, req.Header.Get("X-Idempotency-Key"))
		status := "active"
		switch req.URL.Path {
		case "/v1/actions/onetime-product/create-product":
			if input["storeId"] != appStoreID {
				t.Fatalf("creation used a different store: %v", input)
			}
		case "/v1/actions/onetime-product/update-product":
			if input["id"] != appProductID {
				t.Fatalf("update used a different product: %v", input)
			}
		case "/v1/actions/onetime-product/update-status":
			if input["id"] != appProductID || input["status"] != "inactive" {
				t.Fatalf("delete did not deactivate the selected product: %v", input)
			}
			status = "inactive"
		default:
			t.Fatalf("unexpected provider mutation: %s", req.URL.Path)
		}
		return providerResponse(200, map[string]any{"data": map[string]any{"product": catalogProviderProduct(status)}}), nil
	})}
	a.Payments.Client = client
	in := catalogInputBody()
	createKey := "catalog-create-retry-0001"
	rr := requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, map[string]string{"Idempotency-Key": createKey})
	expectStatus(t, rr, http.StatusOK)
	firstBody := rr.Body.String()
	if strings.Contains(firstBody, appCatalogKey.key) || strings.Contains(firstBody, "MER_") {
		t.Fatal("catalog response exposed merchant credentials")
	}
	a = reopenApp(t, a)
	a.Payments.Client = client
	rr = requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, map[string]string{"Idempotency-Key": createKey})
	expectStatus(t, rr, http.StatusOK)
	if strings.TrimSpace(rr.Body.String()) != strings.TrimSpace(firstBody) || len(mutationPaths) != 1 {
		t.Fatal("catalog retry after restart created another product")
	}
	in["name"] = "Different product"
	expectStatus(t, requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, map[string]string{"Idempotency-Key": createKey}), http.StatusConflict)
	in["name"] = "Fuel"
	expectStatus(t, requestJSON(t, catalogHandler(a), http.MethodPut, "/api/admin/waffo/products/"+appProductID, in, map[string]string{"Idempotency-Key": createKey}), http.StatusConflict)
	if len(mutationPaths) != 1 {
		t.Fatal("conflicting key reuse reached the provider")
	}
	for _, operation := range []struct {
		method string
		key    string
	}{{http.MethodPut, "catalog-update-retry-0001"}, {http.MethodDelete, "catalog-delete-retry-0001"}} {
		for retry := 0; retry < 2; retry++ {
			rr = requestJSON(t, catalogHandler(a), operation.method, "/api/admin/waffo/products/"+appProductID, in, map[string]string{"Idempotency-Key": operation.key})
			expectStatus(t, rr, http.StatusOK)
		}
	}
	if len(mutationPaths) != 3 || providerKeys[0] == providerKeys[1] || providerKeys[1] == providerKeys[2] || countRows(t, a, "idempotency") != 3 {
		t.Fatalf("catalog CRUD retries repeated/conflated provider mutations: paths=%v keys=%v", mutationPaths, providerKeys)
	}
}

func TestWaffoCatalogUncertainRetryExpiresBeforeProviderIdempotencyWindow(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, appWaffoMethod(t))
	calls := 0
	a.Payments.Client = &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return providerResponse(503, map[string]any{"errors": []any{map[string]string{"message": "temporary provider outage"}}}), nil
	})}
	key := "catalog-uncertain-retry-0001"
	in := catalogInputBody()
	headers := map[string]string{"Idempotency-Key": key}
	expectStatus(t, requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, headers), http.StatusBadGateway)
	if _, err := a.DB.Exec("UPDATE idempotency SET created_at=? WHERE key=?", time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339Nano), "catalog_"+key); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, headers), http.StatusConflict)
	if calls != 1 {
		t.Fatal("expired uncertain operation retried after provider key could have expired")
	}
}

func TestWaffoCreateThenFailedPublishResumesAfterRestartWithoutCreatingAgain(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, appWaffoMethod(t))
	creates, publishes, publicationReads := 0, 0, 0
	var createKey, publishKey string
	client := &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/graphql":
			publicationReads++
			var input struct {
				Query     string            `json:"query"`
				Variables map[string]string `json:"variables"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Variables["id"] != appProductID || !strings.Contains(input.Query, "hasProdVersion") {
				t.Fatalf("publication recovery did not read the saved product's state: %#v", input)
			}
			return providerResponse(200, map[string]any{"data": map[string]any{"onetimeProduct": map[string]any{"id": appProductID, "hasProdVersion": false}}}), nil
		case "/v1/actions/onetime-product/create-product":
			creates++
			createKey = req.Header.Get("X-Idempotency-Key")
		case "/v1/actions/onetime-product/publish-product":
			publishes++
			key := req.Header.Get("X-Idempotency-Key")
			if publishKey != "" && key != publishKey {
				t.Fatal("publish recovery changed its provider idempotency key")
			}
			publishKey = key
			if publishes == 1 {
				return providerResponse(503, map[string]any{"errors": []any{map[string]string{"message": "publish temporarily unavailable"}}}), nil
			}
		default:
			t.Fatalf("unexpected request while recovering product creation: %s", req.URL)
		}
		return providerResponse(200, map[string]any{"data": map[string]any{"product": catalogProviderProduct("active")}}), nil
	})}
	a.Payments.Client = client
	in := catalogInputBody()
	in["publish"] = true
	headers := map[string]string{"Idempotency-Key": "catalog-create-publish-resume-0001"}
	expectStatus(t, requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, headers), http.StatusBadGateway)
	if creates != 1 || publishes != 1 {
		t.Fatalf("first catalog request did not reach creation then publication: creates=%d publishes=%d", creates, publishes)
	}
	// Once the product ID is durably known, resuming its publication remains
	// safe even after the provider's create-key retention period has passed.
	if _, err := a.DB.Exec("UPDATE idempotency SET created_at=? WHERE key=?", time.Now().UTC().Add(-25*time.Hour).Format(time.RFC3339Nano), "catalog_"+headers["Idempotency-Key"]); err != nil {
		t.Fatal(err)
	}
	a = reopenApp(t, a)
	a.Payments.Client = client
	rr := requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, headers)
	expectStatus(t, rr, http.StatusOK)
	result := decodeResponse[struct {
		Product payments.WaffoProduct `json:"product"`
	}](t, rr)
	if creates != 1 || publishes != 2 || publicationReads != 1 || createKey == "" || publishKey == "" || createKey == publishKey || result.Product.ID != appProductID || result.Product.HasProdVersion == nil || !*result.Product.HasProdVersion {
		t.Fatalf("restart recreated a product or lost publish state: creates=%d publishes=%d publication_reads=%d product=%#v", creates, publishes, publicationReads, result.Product)
	}
	expectStatus(t, requestJSON(t, catalogHandler(a), http.MethodPost, "/api/admin/waffo/products", in, headers), http.StatusOK)
	if creates != 1 || publishes != 2 || publicationReads != 1 {
		t.Fatal("successful multi-stage retry reached Waffo again")
	}
}
