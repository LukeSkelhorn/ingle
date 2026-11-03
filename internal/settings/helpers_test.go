package settings

import "github.com/lukeskelhorn/domus/internal/store/dbq"

func createPerson(hh int64) dbq.CreatePersonParams {
	return dbq.CreatePersonParams{HouseholdID: hh, Name: "Mum", Kind: "adult", Colour: "#4A90D9", CreatedAt: 1}
}
