package rest

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	corev1 "github.com/murattarslan-dev/tayfam-api/gen/tayfam/core/v1"
)

// specModules reads, from the contract, the Module enum and the property
// names of the Tayfa schema's modules object.
func specModules(t *testing.T) (enum, props []string) {
	t.Helper()
	spec, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(spec), "\n")
	enumRe := regexp.MustCompile(`^      enum: \[(.*)\]$`)
	propRe := regexp.MustCompile(`^            ([a-z_]+): `)
	for i, line := range lines {
		switch line {
		case "    Module:":
			for _, l := range lines[i+1:] {
				if m := enumRe.FindStringSubmatch(l); m != nil {
					for v := range strings.SplitSeq(m[1], ",") {
						enum = append(enum, strings.TrimSpace(v))
					}
					break
				}
				if !strings.HasPrefix(l, "      ") {
					break
				}
			}
		case "    Tayfa:":
			inModules := false
			for _, l := range lines[i+1:] {
				if l != "" && !strings.HasPrefix(l, "      ") {
					break
				}
				if l == "        modules:" {
					inModules = true
					continue
				}
				if !inModules {
					continue
				}
				if m := propRe.FindStringSubmatch(l); m != nil {
					props = append(props, m[1])
					continue
				}
				if strings.HasPrefix(l, "        ") && !strings.HasPrefix(l, "          ") {
					break
				}
			}
		}
	}
	if len(enum) == 0 || len(props) == 0 {
		t.Fatalf("sözleşmeden Module enum'u (%v) ya da Tayfa.modules alanları (%v) okunamadı", enum, props)
	}
	slices.Sort(enum)
	slices.Sort(props)
	return enum, props
}

// TestModulesMatchContract keeps every module in step across the Module
// enum, the Tayfa view's modules object, the proto enum and what gateway
// answers (#316: question_pool was missing from Tayfa.modules).
func TestModulesMatchContract(t *testing.T) {
	enum, props := specModules(t)
	if !slices.Equal(enum, props) {
		t.Errorf("Module enum'u %v, Tayfa.modules alanları %v; aynı olmalı", enum, props)
	}
	if names := slices.Sorted(maps.Keys(modules.byName)); !slices.Equal(names, enum) {
		t.Errorf("gateway modülleri %v, sözleşmedeki Module enum'u %v", names, enum)
	}
	var states []*corev1.ModuleState
	for v := range corev1.Module_name {
		if m := corev1.Module(v); m != corev1.Module_MODULE_UNSPECIFIED {
			states = append(states, &corev1.ModuleState{Module: m, Enabled: true})
		}
	}
	got := slices.Sorted(maps.Keys(tayfaFrom(&corev1.Tayfa{Modules: states}).Modules))
	if !slices.Equal(got, props) {
		t.Errorf("gateway tayfa görünümünde modules %v döndürüyor, sözleşme %v tanımlıyor", got, props)
	}
}
