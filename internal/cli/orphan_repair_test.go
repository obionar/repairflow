package cli

import (
	"path/filepath"
	"testing"
)

func TestOrphanRepairCLIAddAssignAndClear(t *testing.T) {
	home := filepath.Join(t.TempDir(), "ws")
	env := map[string]string{"USER": "tester"}
	base := []string{"--home", home, "--json"}

	if res := runEnv(t, env, append([]string{"init"}, base...)...); res.code != 0 {
		t.Fatalf("init: %+v", res)
	}
	orphan := dataOf(t, runEnv(t, env, append([]string{"repair", "add", "--device", "Laptop", "--issue", "No customer yet"}, base...)...))
	if orphan["customer_id"].(float64) != 0 || orphan["unassigned"] != true || orphan["customer_name"] != nil {
		t.Fatalf("orphan repair: %v", orphan)
	}
	id := int64(orphan["id"].(float64))

	customer := dataOf(t, runEnv(t, env, append([]string{"customer", "add", "--name", "Ada Lovelace"}, base...)...))
	cid := int64(customer["id"].(float64))
	assigned := dataOf(t, runEnv(t, env, append([]string{"repair", "update", itoa(id), "--customer", itoa(cid)}, base...)...))
	if assigned["customer_id"].(float64) != float64(cid) || assigned["unassigned"] != nil || assigned["customer_name"] != "Ada Lovelace" {
		t.Fatalf("assigned repair: %v", assigned)
	}

	cleared := dataOf(t, runEnv(t, env, append([]string{"repair", "update", itoa(id), "--no-customer"}, base...)...))
	if cleared["customer_id"].(float64) != 0 || cleared["unassigned"] != true {
		t.Fatalf("cleared repair: %v", cleared)
	}
}
