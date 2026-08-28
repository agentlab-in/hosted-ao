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
				Endpoint       string   `json:"endpoint"`
				ResponseFields []string `json:"responseFields"`
				CheckFields    []string `json:"checkFields"`
				CheckIDs       string   `json:"checkIDs"`
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
		if got := jsonFieldNames(reflect.TypeOf(DoctorReportResponse{})); !reflect.DeepEqual(got, iface.WireContract.ResponseFields) {
			t.Fatalf("DoctorReportResponse fields = %v, contract = %v", got, iface.WireContract.ResponseFields)
		}
		if got := jsonFieldNames(reflect.TypeOf(DoctorCheckResponse{})); !reflect.DeepEqual(got, iface.WireContract.CheckFields) {
			t.Fatalf("DoctorCheckResponse fields = %v, contract = %v", got, iface.WireContract.CheckFields)
		}
		return
	}
	t.Fatal("compatibility contract is missing ao-daemon-api")
}

func jsonFieldNames(typ reflect.Type) []string {
	fields := make([]string, 0, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields = append(fields, name)
		}
	}
	return fields
}
