package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/pkg/mcpclient"
)

const (
	defaultEndpoint   = "http://localhost:8080/mcp"
	defaultTenantID   = "tenant-finops-prod"
	defaultEngineerID = "eng-oncall-commander"
	defaultTokenTTL   = 1 * time.Hour
)

func main() {
	var (
		endpoint    = flag.String("endpoint", envOrDefault("MCP_ENDPOINT", defaultEndpoint), "Stateless MCP server endpoint URL")
		token       = flag.String("token", os.Getenv("MCP_BEARER_TOKEN"), "Pre-signed Bearer JWT token")
		jwtSecret   = flag.String("jwt-secret", os.Getenv("JWT_SIGNING_SECRET"), "HMAC secret to mint a JWT if -token is not provided")
		tenantID    = flag.String("tenant", defaultTenantID, "Tenant ID claim when minting a JWT")
		engineerID  = flag.String("engineer", defaultEngineerID, "Engineer ID claim when minting a JWT")
		mode        = flag.String("mode", "demo", "Execution mode: demo | discover | list")
		incidentID  = flag.String("incident-id", "INC-2026-101", "Incident ID for demo workflow")
		autoApprove = flag.Bool("auto-approve", true, "Automatically approve SEP-2322 InputRequiredResult prompts")
	)
	flag.Parse()

	bearer := strings.TrimSpace(*token)
	if bearer == "" {
		secret := strings.TrimSpace(*jwtSecret)
		if secret == "" {
			fmt.Fprintln(os.Stderr, "error: either -token (MCP_BEARER_TOKEN) or -jwt-secret (JWT_SIGNING_SECRET) must be provided")
			os.Exit(1)
		}
		var err error
		bearer, err = middleware.GenerateToken([]byte(secret), *tenantID, *engineerID, defaultTokenTTL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error generating JWT: %v\n", err)
			os.Exit(1)
		}
	}

	client, err := mcpclient.New(mcpclient.Config{
		Endpoint:    *endpoint,
		BearerToken: bearer,
		ProgressHandler: func(notif mcp.ProgressNotificationParams) {
			fmt.Printf("  [SSE Progress %3d%%] stage=%s | %s\n", notif.Progress, notif.Stage, notif.Message)
		},
		ApprovalHandler: func(_ context.Context, inputReq mcp.InputRequiredResult) (bool, string, error) {
			fmt.Printf("  [SEP-2322 Input Required] token=%s\n  Reason: %s\n", inputReq.ContinuationToken, inputReq.Reason)
			if *autoApprove {
				fmt.Println("  [Auto-Approve] Sending 2nd round-trip approval (approved=true)...")
				return true, "Auto-approved by CLI commander", nil
			}
			fmt.Print("  Approve high-risk/cost mitigation? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			decision := strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
			return decision, "Interactive CLI decision", nil
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create MCP client: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch strings.ToLower(strings.TrimSpace(*mode)) {
	case "discover":
		runDiscover(ctx, client)
	case "list":
		runListTools(ctx, client)
	case "demo":
		runFullDemo(ctx, client, *incidentID)
	default:
		fmt.Fprintf(os.Stderr, "unsupported mode %q\n", *mode)
		os.Exit(1)
	}
}

func runDiscover(ctx context.Context, client *mcpclient.Client) {
	disc, err := client.Discover(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "server/discover failed: %v\n", err)
		os.Exit(1)
	}
	printJSON("1. Stateless Server Discovery (server/discover)", disc)
}

func runListTools(ctx context.Context, client *mcpclient.Client) {
	list, err := client.ListTools(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tools/list failed: %v\n", err)
		os.Exit(1)
	}
	printJSON("2. Tools List with SEP-2549 Caching (tools/list)", list)
}

func runFullDemo(ctx context.Context, client *mcpclient.Client, incidentID string) {
	runDiscover(ctx, client)
	runListTools(ctx, client)

	// Tool 1: incident_context_manager (create + append_note + get)
	createRes, err := client.CallTool(ctx, mcp.ToolIncidentContextManager, map[string]any{
		"action":      "create",
		"incident_id": incidentID,
		"title":       "Production RDS Multi-AZ Failover & NAT Cost Spike",
		"severity":    "critical",
		"note":        "Incident opened via Stateless MCP CLI",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "incident create failed: %v\n", err)
		os.Exit(1)
	}
	printJSON("3. Tool 1: incident_context_manager (create)", createRes)

	// Tool 2: execute_mitigation_plan (SEP-2322 Multi-Round-Trip)
	mitigateRes, err := client.CallTool(ctx, mcp.ToolExecuteMitigationPlan, map[string]any{
		"incident_id":        incidentID,
		"action":             "failover_rds_cluster_and_scale_read_replicas",
		"target_resource":    "arn:aws:rds:eu-central-1:123456789012:cluster:prod-ledger",
		"risk_level":         "high",
		"estimated_cost_usd": 1250.00,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "execute_mitigation_plan failed: %v\n", err)
		os.Exit(1)
	}
	printJSON("4. Tool 2: execute_mitigation_plan (SEP-2322 Completed after 2nd Round-Trip)", mitigateRes)

	// Tool 3: live_telemetry_diagnostic (Streamable HTTP / Chunked SSE)
	fmt.Println("\n=== 5. Tool 3: live_telemetry_diagnostic (Streamable HTTP / SSE) ===")
	telemetryRes, err := client.CallTool(ctx, mcp.ToolLiveTelemetryDiagnostic, map[string]any{
		"incident_id":    incidentID,
		"target_service": "prod-ledger-api",
		"window_minutes": 15,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "live_telemetry_diagnostic failed: %v\n", err)
		os.Exit(1)
	}
	printJSON("5. Tool 3 Final Result (after SSE stream closed)", telemetryRes)
}

func printJSON(title string, v any) {
	fmt.Printf("\n=== %s ===\n", title)
	encoded, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(encoded))
}

func envOrDefault(key, fallback string) string {
	if val := strings.TrimSpace(os.Getenv(key)); val != "" {
		return val
	}
	return fallback
}
