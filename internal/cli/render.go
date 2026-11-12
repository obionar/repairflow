package cli

import (
	"fmt"

	"repairflow/internal/app"
	"repairflow/internal/backup"
	"repairflow/internal/buildinfo"
	"repairflow/internal/seed"
	"repairflow/internal/service"
	"repairflow/internal/update"
	"repairflow/internal/workspace"
)

// renderData renders known result types for human output. Unknown types fall
// back to JSON so new data can never render as nothing.
func renderData(data any) (string, bool) {
	switch v := data.(type) {
	case *workspace.InitResult:
		if v.AlreadyInitialized {
			return fmt.Sprintf("workspace already initialized: %s\n", v.Home), true
		}
		return fmt.Sprintf("initialized workspace: %s\nconfig: %s\n", v.Home, v.ConfigPath), true
	case *workspace.Status:
		return fmt.Sprintf("home: %s\ninitialized: %t\nconfig_version: %d\napp.environment: %s\nweb: %s:%d\n",
			v.Home, v.Initialized, v.Config.ConfigVersion, v.Config.App.Environment, v.Config.Web.Host, v.Config.Web.Port), true
	case buildinfo.Info:
		return fmt.Sprintf("repairflow %s\ncommit: %s\nbuild: %s\ngo: %s\nplatform: %s\n",
			v.Version, v.Commit, v.BuildDate, v.GoVersion, v.Platform), true
	case app.Customer:
		return renderCustomer(v), true
	case customerShow:
		out := renderCustomer(v.Customer)
		out += "history:\n"
		for _, h := range v.History {
			out += fmt.Sprintf("  #%d %s %s by %s: %s\n", h.ID, h.CreatedAt, h.Action, h.Actor, h.Detail)
		}
		return out, true
	case app.Page[app.Customer]:
		out := fmt.Sprintf("customers: %d total (page %d, %d per page)\n", v.Total, v.Page, v.PerPage)
		for _, c := range v.Items {
			out += fmt.Sprintf("  #%d %s", c.ID, c.Name)
			if c.Phone != "" {
				out += " · " + c.Phone
			}
			if c.Email != "" {
				out += " · " + c.Email
			}
			out += "\n"
		}
		return out, true
	case []app.CustomerHistory:
		out := ""
		for _, h := range v {
			out += fmt.Sprintf("#%d %s %s by %s: %s\n", h.ID, h.CreatedAt, h.Action, h.Actor, h.Detail)
		}
		if out == "" {
			out = "no history\n"
		}
		return out, true
	case app.Repair:
		return renderRepair(v), true
	case app.Page[app.Repair]:
		out := fmt.Sprintf("repairs: %d total (page %d, %d per page)\n", v.Total, v.Page, v.PerPage)
		for _, t := range v.Items {
			out += fmt.Sprintf("  #%d [%s] %s — %s (%s)\n", t.ID, t.Stage, t.Device, t.Issue, t.CustomerName)
		}
		return out, true
	case app.RepairDetail:
		out := renderRepair(v.Repair)
		out += "log:\n"
		for _, e := range v.Entries {
			mark := ""
			if e.DeletedAt != "" {
				mark = " [DELETED]"
			}
			out += fmt.Sprintf("  #%d %s [%s] %s%s\n", e.ID, e.CreatedAt, e.Kind, e.Body, mark)
		}
		if len(v.Entries) == 0 {
			out += "  (empty)\n"
		}
		out += "activity:\n"
		for _, a := range v.Activity {
			out += fmt.Sprintf("  #%d %s %s by %s: %s\n", a.ID, a.CreatedAt, a.Action, a.Actor, a.Detail)
		}
		return out, true
	case *backup.Result:
		return renderResult(v), true
	case *backup.Manifest:
		return renderManifest("verified", v), true
	case restoreResult:
		out := "restored workspace: " + v.Home + "\n"
		out += renderManifest("restored", &v.Manifest)
		return out, true
	case []app.Attachment:
		out := ""
		for _, a := range v {
			out += renderAttachment(a)
		}
		if out == "" {
			out = "no attachments\n"
		}
		return out, true
	case app.Attachment:
		return renderAttachment(v), true
	case exportResult:
		return fmt.Sprintf("exported %d bytes to %s (sha256 %s)\n", v.SizeBytes, v.Path, v.SHA256), true
	case app.Entry:
		line := fmt.Sprintf("entry #%d on repair #%d [%s] %s: %s\n", v.ID, v.RepairID, v.CreatedAt, v.Kind, v.Body)
		if v.DeletedAt != "" {
			line = fmt.Sprintf("entry #%d [DELETED %s] [%s] %s: %s\n", v.ID, v.DeletedAt, v.CreatedAt, v.Kind, v.Body)
		}
		return line, true
	case app.Part:
		return renderPart(v), true
	case []app.Part:
		out := ""
		for _, p := range v {
			out += renderPart(p)
		}
		if out == "" {
			out = "no part lines\n"
		}
		return out, true
	case app.Estimate:
		return renderEstimate(v), true
	case []app.Estimate:
		out := ""
		for _, e := range v {
			out += renderEstimate(e)
		}
		if out == "" {
			out = "no estimates\n"
		}
		return out, true
	case seed.Result:
		return renderSeedText(v), true
	case *seed.ResetResult:
		out := "demo workspace reset to pristine state\n"
		text, _ := renderData(v.Seeded)
		return out + text, true
	case doctorReport:
		out := ""
		for _, c := range v.Checks {
			mark := "ok  "
			if !c.OK {
				mark = "FAIL"
			}
			out += fmt.Sprintf("%s %s: %s\n", mark, c.Name, c.Detail)
		}
		if v.OK {
			out += "doctor: healthy\n"
		} else {
			out += "doctor: problems found\n"
		}
		return out, true
	case *update.CheckResult:
		out := fmt.Sprintf("installed: %s\navailable: %s (%s)\nartifact: %s %s %s (%d bytes)\n",
			v.CurrentVersion, v.NewVersion, v.Channel, v.Artifact.OS, v.Artifact.Arch, v.Artifact.File, v.Artifact.SizeBytes)
		if v.UpToDate {
			out += "status: up to date\n"
		} else {
			out += "status: update available (offline install via update apply)\n"
		}
		return out, true
	case *update.ApplyResult:
		return fmt.Sprintf("updated %s -> %s\nprevious binary preserved: %s\n%s\n",
			v.FromVersion, v.ToVersion, v.BackupPath, v.RestartNote), true
	case *keygenResult:
		return fmt.Sprintf("public key: %s (%s)\nprivate key: %s\nwarning: %s\n",
			v.PublicKeyFile, v.PublicKeyHex, v.PrivateKeyFile, v.Warning), true
	case *signResult:
		return fmt.Sprintf("signature written: %s (key %s)\n", v.SignatureFile, v.PublicKeyHex), true
	case *service.Result:
		out := "unit: " + v.UnitPath + " (present=" + itoaBool(v.Present) + ")"
		if v.UnitPath == "" {
			out = "unit: none (present=false)"
		}
		if v.Active != "" {
			out += " active=" + v.Active
		}
		if v.StagedPath != "" {
			out += "\nstaged: " + v.StagedPath
		}
		if v.Warning != "" {
			out += "\nwarning: " + v.Warning
		}
		if v.Command != "" {
			out += "\ncommand: " + v.Command
		}
		return out + "\n", true
	case *workspace.CleanupResult:
		return fmt.Sprintf("cleanup: removed %d staging file(s), %d cache entr(ies), kept %d\n",
			len(v.StagingRemoved), len(v.CacheRemoved), len(v.Skipped)), true
	case statusReport:
		return fmt.Sprintf("repairflow %s\nhome: %s\ninitialized: %t\nconfig_version: %d\nschema_version: %d\nrecords: %d customers, %d repairs\ndemo_seeded: %t\n",
			v.Version, v.Home, v.Initialized, v.ConfigVersion, v.SchemaVersion, v.Customers, v.Repairs, v.DemoSeeded), true
	}
	return "", false
}

func renderCustomer(c app.Customer) string {
	return fmt.Sprintf("customer #%d %s\nphone: %s\nemail: %s\naddress: %s\nnotes: %s\nrevision: %s\ncreated: %s\nupdated: %s\n",
		c.ID, c.Name, c.Phone, c.Email, c.Address, c.Notes, c.Revision, c.CreatedAt, c.UpdatedAt)
}

func renderRepair(t app.Repair) string {
	out := fmt.Sprintf("repair #%d [%s] %s\ncustomer: #%d %s\ndevice_type: %s\nbrand: %s\nmodel: %s\nserial: %s\nissue: %s\nnotes: %s\nrevision: %s\ncreated: %s\nupdated: %s\n",
		t.ID, t.Stage, t.Device, t.CustomerID, t.CustomerName, t.DeviceType, t.Brand, t.Model,
		t.Serial, t.Issue, t.Notes, t.Revision, t.CreatedAt, t.UpdatedAt)
	return out
}
