package seed

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
)

// Synthetic demo media. All samples are generated in code from
// fixed patterns — redistributable by construction, deterministic across
// runs, and clearly synthetic in appearance. No real photos or third-party
// assets are used anywhere in the demo dataset.

type mediaSample struct {
	fileName string
	caption  string
	source   string
	repair   int // index into demoData.repairs
	entry    int // -1 = intake (no log entry); otherwise entry index on that repair
	variant  int // distinct pixel pattern per sample (keeps files byte-unique)
}

// demoMedia is attached during seeding. At least one photo lands on intake
// and one on a log entry. Variants keep every sample
// byte-distinct so the demo never shows two identical files.
var demoMedia = []mediaSample{
	{fileName: "intake_front_of_mystery.png", caption: "Intake photo: the mystery before the screwdriver", source: "demo", repair: 0, entry: -1, variant: 0},
	{fileName: "burnt_toast_jack.png", caption: "Evidence: the charging port formerly known as a port", source: "demo", repair: 0, entry: 0, variant: 1},
	{fileName: "screen_crack_constellation.png", caption: "Crack pattern: accidental modern art", source: "demo", repair: 1, entry: -1, variant: 2},
	{fileName: "wiring_schematic_ish.pdf", caption: "Service diagram: mostly wires, partly hope", source: "demo", repair: 2, entry: 1, variant: 3},
	{fileName: "firmware_backup_do_not_panic.bin", caption: "Firmware dump: synthetic, like the confidence", source: "demo", repair: 3, entry: -1, variant: 4},
	{fileName: "group_head_pressure.png", caption: "Pressure test: espresso strong enough to rename the cat", source: "demo", repair: 5, entry: 1, variant: 5},
	{fileName: "water_damage_residue.png", caption: "Residue on the right-channel board; the left channel declined comment", source: "demo", repair: 6, entry: 0, variant: 6},
	// Bulk-pack samples: intake photos and bench evidence on generated repairs.
	{fileName: "bulk_intake_laptop.png", caption: "Bulk intake: another Tuesday, another mystery", source: "demo", repair: 8, entry: -1, variant: 7},
	{fileName: "bulk_bench_phone.png", caption: "Bench evidence: phone and technician both still booting", source: "demo", repair: 12, entry: 0, variant: 8},
	{fileName: "bulk_intake_console.png", caption: "Console intake: it has seen things", source: "demo", repair: 17, entry: -1, variant: 9},
}

// demoPNG renders a deterministic synthetic "photo": a labeled-looking
// pattern block. Fixed pixels -> identical bytes on every run.
func demoPNG(seed int) []byte {
	const w, h = 240, 160
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Diagonal gradient with a deterministic "device body" block.
			r := uint8((x*7 + seed*53) % 256)
			g := uint8((y*5 + seed*29) % 256)
			b := uint8((x + y + seed*11) % 256)
			if x > 40 && x < w-40 && y > 30 && y < h-30 {
				r, g, b = 40, 44, 52 // device body
			}
			// A "damage mark" bar so images differ recognizably.
			if y > 60+seed*8 && y < 76+seed*8 && x > 60 && x < w-60 {
				r, g, b = 200, 60, 40
			}
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	var buf bytes.Buffer
	// png.Encoder with default settings is deterministic for identical input.
	if err := png.Encode(&buf, img); err != nil {
		panic("seed: cannot encode demo png: " + err.Error())
	}
	return buf.Bytes()
}

// demoPDF is a minimal, valid-looking PDF placeholder (synthetic).
func demoPDF() []byte {
	body := `%PDF-1.4
1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj
2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj
3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >> endobj
trailer << /Root 1 0 R >>
%%EOF
`
	return []byte(body)
}

// demoFirmware is a synthetic binary blob (clearly not a real dump).
func demoFirmware() []byte {
	out := []byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}
	out = append(out, []byte("REPAIRFLOW SAMPLE FIRMWARE (SYNTHETIC)")...)
	for i := 0; i < 256; i++ {
		out = append(out, byte((i*37+11)%256))
	}
	return out
}

// mediaPayload returns the deterministic bytes for a sample.
func (m mediaSample) mediaPayload() []byte {
	switch {
	case bytes.HasSuffix([]byte(m.fileName), []byte(".png")):
		return demoPNG(m.variant)
	case bytes.HasSuffix([]byte(m.fileName), []byte(".pdf")):
		return demoPDF()
	default:
		return demoFirmware()
	}
}
