package multisig

import (
	"encoding/json"
	"fmt"
	"math/big"

	"steemvm/oracle/go/steemtx"
)

// SelftestApportion checks Apportion against every apportionment vector in
// oracle/multisig/vectors.json. The signer refuses to start unless it passes:
// two signers that compute different authorities can never agree on a
// validator-set change.
func SelftestApportion(vectorsJSON []byte) error {
	var v struct {
		Apportionment []struct {
			Name             string `json:"name"`
			ThresholdPercent uint32 `json:"threshold_percent"`
			Validators       []struct {
				Account string `json:"account"`
				Tokens  string `json:"tokens"`
			} `json:"validators"`
			Expected steemtx.Authority `json:"expected"`
		} `json:"apportionment"`
	}
	if err := json.Unmarshal(vectorsJSON, &v); err != nil {
		return fmt.Errorf("selftest apportionment: parse: %w", err)
	}
	if len(v.Apportionment) == 0 {
		return fmt.Errorf("selftest apportionment: no cases")
	}
	for _, c := range v.Apportionment {
		entries := make([]StakeEntry, 0, len(c.Validators))
		for _, val := range c.Validators {
			tok, ok := new(big.Int).SetString(val.Tokens, 10)
			if !ok {
				return fmt.Errorf("selftest apportionment %q: bad tokens %q", c.Name, val.Tokens)
			}
			entries = append(entries, StakeEntry{Account: val.Account, Tokens: tok})
		}
		got, err := Apportion(entries, c.ThresholdPercent)
		if err != nil {
			return fmt.Errorf("selftest apportionment %q: %w", c.Name, err)
		}
		want := c.Expected
		if want.KeyAuths == nil {
			want.KeyAuths = []steemtx.KeyAuth{}
		}
		if !got.Equal(want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(want)
			return fmt.Errorf("selftest apportionment %q:\n got  %s\n want %s", c.Name, gj, wj)
		}
	}
	return nil
}
