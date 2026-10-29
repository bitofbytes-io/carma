package server

import (
	"encoding/csv"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/bitofbytes-io/carma/internal/model"
	"github.com/bitofbytes-io/carma/internal/repository"
	"github.com/google/uuid"
)

func TestRecordWithPDFRangeAndFilteredCSV(t *testing.T) {
	f := setup(t)
	v := createVehicle(t, f)
	types, _ := f.store.ListServiceTypes(t.Context())
	oil := types[0]
	for _, x := range types {
		if x.Name == "Oil change" {
			oil = x
		}
	}
	pdf := []byte("%PDF-1.7\nreceipt fixture bytes")
	b, ct := formBody(t, map[string]string{"occurred_on": "2026-07-30", "service_type_id": oil.ID.String(), "odometer": "62410", "cost": "89.00", "vendor": "Jiffy Lube", "notes": "full synthetic"}, "receipt.pdf", pdf)
	w := f.do(t, "POST", "/vehicles/"+v.ID.String()+"/records", b, ct)
	if w.Code != 303 {
		t.Fatalf("record: %d %s", w.Code, w.Body.String())
	}
	rows, _ := f.store.ListRecords(t.Context(), model.RecordQuery{VehicleID: &v.ID, Search: "Jiffy", ServiceTypeID: &oil.ID})
	if len(rows) != 1 || rows[0].AttachmentCount != 1 {
		t.Fatalf("rows=%+v", rows)
	}
	_, as, _ := f.store.GetRecord(t.Context(), rows[0].ID)
	detail := f.do(t, "GET", "/records/"+rows[0].ID.String(), nil, "")
	assertHTMXConfirmedForm(t, detail.Body.String(), "/records/"+rows[0].ID.String()+"/delete", "Delete this record and its receipts?")
	assertHTMXConfirmedForm(t, detail.Body.String(), "/attachments/"+as[0].ID.String()+"/delete", "Delete this receipt?")
	req := httptest.NewRequest("GET", "http://example.com/attachments/"+as[0].ID.String(), nil)
	req.AddCookie(f.cookie)
	req.Header.Set("Range", "bytes=0-4")
	rw := httptest.NewRecorder()
	f.router.ServeHTTP(rw, req)
	if rw.Code != http.StatusPartialContent || rw.Body.String() != "%PDF-" {
		t.Fatalf("range: %d %q", rw.Code, rw.Body.String())
	}
	csvW := f.do(t, "GET", "/vehicles/"+v.ID.String()+"/export.csv?q=Jiffy&type="+oil.ID.String(), nil, "")
	parsed, e := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvW.Body.String(), "\ufeff"))).ReadAll()
	if e != nil || len(parsed) != 2 || parsed[1][5] != "Jiffy Lube" {
		t.Fatalf("csv=%v err=%v", parsed, e)
	}
	unauth := httptest.NewRecorder()
	f.router.ServeHTTP(unauth, httptest.NewRequest("GET", "http://example.com/attachments/"+as[0].ID.String(), nil))
	if unauth.Code != http.StatusSeeOther || !strings.HasPrefix(unauth.Header().Get("Location"), "/login?redirect=") {
		t.Fatalf("attachment auth: %d %s", unauth.Code, unauth.Header().Get("Location"))
	}
	notFound := f.do(t, "GET", "/attachments/"+uuid.NewString(), nil, "")
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("attachment not found: %d", notFound.Code)
	}
	deleted := f.do(t, "POST", "/attachments/"+as[0].ID.String()+"/delete", strings.NewReader(""), "application/x-www-form-urlencoded")
	if deleted.Code != http.StatusSeeOther || deleted.Header().Get("Location") != "/records/"+rows[0].ID.String() {
		t.Fatalf("attachment delete redirect: %d %s", deleted.Code, deleted.Header().Get("Location"))
	}
	if _, _, err := f.store.GetAttachment(t.Context(), as[0].ID); err != repository.ErrNotFound {
		t.Fatalf("attachment metadata remains: %v", err)
	}
	if object, err := f.s.assets.Open(t.Context(), as[0].StorageKey); err == nil {
		_ = object.Close()
		t.Fatalf("attachment asset remains at %q", as[0].StorageKey)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checking removed attachment asset: %v", err)
	}
}
