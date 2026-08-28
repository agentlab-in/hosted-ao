package controllers

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHAODaemonDoctorWireContractMatchesDTO(t *testing.T) {
	path := filepath.Join("..", "..", "..", "..", "contracts", "hao", "v1", "compatibility.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Interfaces []struct {
			ID           string `json:"id"`
			Versions     []int  `json:"versions"`
			WireContract struct {
				Endpoint       string           `json:"endpoint"`
				ResponseFields []wireFieldShape `json:"responseFields"`
				CheckFields    []wireFieldShape `json:"checkFields"`
				CheckIDs       string           `json:"checkIDs"`
			} `json:"wireContract"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	for _, iface := range contract.Interfaces {
		if iface.ID != "ao-daemon-api" {
			continue
		}
		if len(iface.Versions) != 0 || iface.WireContract.Endpoint != "/api/v1/doctor" || iface.WireContract.CheckIDs != "daemon-defined-unversioned" {
			t.Fatalf("daemon doctor compatibility must describe the current unversioned wire: %+v", iface)
		}
		if got := jsonFieldShapes(reflect.TypeOf(DoctorReportResponse{})); !reflect.DeepEqual(got, iface.WireContract.ResponseFields) {
			t.Fatalf("DoctorReportResponse fields = %v, contract = %v", got, iface.WireContract.ResponseFields)
		}
		if got := jsonFieldShapes(reflect.TypeOf(DoctorCheckResponse{})); !reflect.DeepEqual(got, iface.WireContract.CheckFields) {
			t.Fatalf("DoctorCheckResponse fields = %v, contract = %v", got, iface.WireContract.CheckFields)
		}
		return
	}
	t.Fatal("compatibility contract is missing ao-daemon-api")
}

type wireFieldShape struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

func jsonFieldShapes(typ reflect.Type) []wireFieldShape {
	fields := make([]wireFieldShape, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		parts := strings.Split(field.Tag.Get("json"), ",")
		name := parts[0]
		if name != "" && name != "-" {
			required := true
			for _, option := range parts[1:] {
				if option == "omitempty" {
					required = false
				}
			}
			fields = append(fields, wireFieldShape{Name: name, Type: wireType(field.Type), Required: required})
		}
	}
	return fields
}

func wireType(typ reflect.Type) string {
	switch typ.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.String:
		return "string"
	case reflect.Slice:
		return "array<" + wireTypeName(typ.Elem()) + ">"
	default:
		return wireTypeName(typ)
	}
}

func wireTypeName(typ reflect.Type) string {
	if typ.Name() != "" {
		return typ.Name()
	}
	return typ.String()
}
