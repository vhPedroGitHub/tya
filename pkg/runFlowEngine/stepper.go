package runflowengine

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vhPedroGitHub/tya/pkg/configyml"
)

// StepThroughSteps presents an interactive stepper for a test-mode flow run.
// It runs in the same terminal and allows navigating forwards/backwards
// through each executed step, inspecting request and response payloads.
func StepThroughSteps(flow configyml.Flow, detailed []stepResult) {
	if len(detailed) == 0 {
		return
	}

	reader := bufio.NewReader(os.Stdin)
	idx := 0

	clear := func() {
		fmt.Print("\033[H\033[2J")
	}

	pretty := func(b []byte) string {
		if len(b) == 0 {
			return "(empty)"
		}
		s := strings.TrimSpace(string(b))
		if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
			var out bytes.Buffer
			if err := json.Indent(&out, b, "", "  "); err == nil {
				return out.String()
			}
		}
		return s
	}

	for {
		clear()
		cur := detailed[idx]
		step := flow.Steps[idx]
		fmt.Printf("Flow: %s — Step %d/%d — %s %s\n", flow.Name, idx+1, len(detailed), step.Method, step.Endpoint)
		fmt.Println("-------------------------------------------------------------------")
		fmt.Printf("Request:\n%s\n\n", pretty(cur.RequestBody))
		fmt.Printf("Response: status=%d\n%s\n\n", cur.StatusCode, pretty(cur.Body))
		fmt.Println("Commands: n=next, p=prev, f=first, l=last, j <num>=jump, q=quit")
		fmt.Print("> ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" || line == "n" {
			if idx < len(detailed)-1 {
				idx++
			}
			continue
		}
		switch {
		case line == "p":
			if idx > 0 {
				idx--
			}
		case line == "f":
			idx = 0
		case line == "l":
			idx = len(detailed) - 1
		case line == "q":
			clear()
			return
		case strings.HasPrefix(line, "j "):
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if n, err := strconv.Atoi(parts[1]); err == nil {
					if n >= 1 && n <= len(detailed) {
						idx = n - 1
					}
				}
			}
		default:
			// ignore unknown command
		}
	}
}
