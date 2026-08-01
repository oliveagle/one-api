package ratio

import "testing"

// The image ratio tables are referenced by name from the image-generation
// pipeline to decide per-image cost. If any of the seeded models or sizes
// disappear silently, every prompt of that size bills at the wrong rate.
func TestImageSizeRatios_KnownModelsAndSizes(t *testing.T) {
	cases := []struct {
		model string
		size  string
		want  float64
	}{
		{"dall-e-2", "256x256", 1},
		{"dall-e-2", "1024x1024", 1.25},
		{"dall-e-3", "1024x1024", 1},
		{"dall-e-3", "1024x1792", 2},
		{"ali-stable-diffusion-xl", "512x1024", 1},
		{"wanx-v1", "720x1280", 1},
		{"step-1x-medium", "256x256", 1},
	}
	for _, tc := range cases {
		got, ok := ImageSizeRatios[tc.model][tc.size]
		if !ok {
			t.Errorf("missing ratio for %q size %q", tc.model, tc.size)
			continue
		}
		if got != tc.want {
			t.Errorf("%q/%q = %v want %v", tc.model, tc.size, got, tc.want)
		}
	}
}

// ImageGenerationAmounts pins down the [min, max] supported image counts per
// model. If DALL-E-3's max drops from 1 to 0 silently, callers will be refused
// invalid requests instead of billed.
func TestImageGenerationAmounts_Bounds(t *testing.T) {
	cases := []struct {
		model string
		min   int
		max   int
	}{
		{"dall-e-2", 1, 10},
		{"dall-e-3", 1, 1},
		{"ali-stable-diffusion-xl", 1, 4},
		{"wanx-v1", 1, 4},
		{"cogview-3", 1, 1},
		{"step-1x-medium", 1, 1},
	}
	for _, tc := range cases {
		bounds, ok := ImageGenerationAmounts[tc.model]
		if !ok {
			t.Errorf("missing amounts for %q", tc.model)
			continue
		}
		if bounds[0] != tc.min || bounds[1] != tc.max {
			t.Errorf("%q = [%d,%d] want [%d,%d]", tc.model, bounds[0], bounds[1], tc.min, tc.max)
		}
	}
}

func TestImagePromptLengthLimitations(t *testing.T) {
	cases := map[string]int{
		"dall-e-2":                1000,
		"dall-e-3":                4000,
		"ali-stable-diffusion-xl": 4000,
		"cogview-3":               833,
	}
	for model, want := range cases {
		got, ok := ImagePromptLengthLimitations[model]
		if !ok {
			t.Errorf("missing limit for %q", model)
			continue
		}
		if got != want {
			t.Errorf("%q limit = %d want %d", model, got, want)
		}
	}
}

// ImageOriginModelName translates the one-API alias into the upstream name.
// If a translation disappears the upstream gets the wrong model id and the
// call 404s.
func TestImageOriginModelName(t *testing.T) {
	cases := map[string]string{
		"ali-stable-diffusion-xl":   "stable-diffusion-xl",
		"ali-stable-diffusion-v1.5": "stable-diffusion-v1.5",
	}
	for alias, want := range cases {
		got, ok := ImageOriginModelName[alias]
		if !ok {
			t.Errorf("missing alias %q", alias)
			continue
		}
		if got != want {
			t.Errorf("%q = %q want %q", alias, got, want)
		}
	}
	// dall-e-* deliberately has no alias and must remain absent.
	if _, ok := ImageOriginModelName["dall-e-3"]; ok {
		t.Error("dall-e-3 must not have an origin alias")
	}
}
