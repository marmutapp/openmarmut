package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/marmutapp/openmarmut/internal/config"
	"github.com/marmutapp/openmarmut/internal/mcp"
	"github.com/marmutapp/openmarmut/internal/ui"
	"github.com/spf13/cobra"
)

func newMCPCmd(runner *Runner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Manage MCP (Model Context Protocol) servers",
	}

	cmd.AddCommand(
		newMCPListCmd(runner),
		newMCPAddCmd(runner),
		newMCPTestCmd(runner),
	)

	return cmd
}

func newMCPListCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers and their tools",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("mcp list: %w", err)
			}

			if len(cfg.MCP.Servers) == 0 {
				fmt.Fprintln(os.Stderr, ui.FormatHint("No MCP servers configured. Add servers in .openmarmut.yaml under mcp.servers."))
				return nil
			}

			headers := []string{"NAME", "TRANSPORT", "ENDPOINT", "STATUS"}
			var rows [][]string

			for _, s := range cfg.MCP.Servers {
				endpoint := s.URL
				if s.Transport == "stdio" {
					endpoint = s.Command
					if len(s.Args) > 0 {
						endpoint += " " + s.Args[0]
						if len(s.Args) > 1 {
							endpoint += "..."
						}
					}
				}
				if len(endpoint) > 50 {
					endpoint = endpoint[:50] + "..."
				}
				rows = append(rows, []string{s.Name, s.Transport, endpoint, "configured"})
			}

			fmt.Fprintln(os.Stdout, ui.RenderTable(headers, rows, -1))
			return nil
		},
	}
}

func newMCPAddCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add an SSE MCP server to config",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			url := args[1]

			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("mcp add: %w", err)
			}

			// Check for duplicate name.
			for _, s := range cfg.MCP.Servers {
				if s.Name == name {
					return fmt.Errorf("mcp add: server %q already exists", name)
				}
			}

			newServer := mcp.MCPServerConfig{
				Name:      name,
				Transport: "sse",
				URL:       url,
			}

			fmt.Fprintf(os.Stderr, "Add the following to your .openmarmut.yaml:\n\n")
			fmt.Fprintf(os.Stdout, "mcp:\n  servers:\n")
			for _, s := range cfg.MCP.Servers {
				fmt.Fprintf(os.Stdout, "    - name: %s\n      transport: %s\n", s.Name, s.Transport)
				if s.Transport == "sse" {
					fmt.Fprintf(os.Stdout, "      url: %q\n", s.URL)
				}
			}
			fmt.Fprintf(os.Stdout, "    - name: %s\n      transport: %s\n      url: %q\n",
				newServer.Name, newServer.Transport, newServer.URL)

			fmt.Fprintln(os.Stderr, ui.FormatSuccess(fmt.Sprintf("Server %q ready to add. Copy the config above into .openmarmut.yaml.", name)))
			return nil
		},
	}
}

func newMCPTestCmd(runner *Runner) *cobra.Command {
	return &cobra.Command{
		Use:   "test <name>",
		Short: "Test connection to an MCP server and list its tools",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			cfg, err := config.Load(runner.flags)
			if err != nil {
				return fmt.Errorf("mcp test: %w", err)
			}

			var serverCfg *mcp.MCPServerConfig
			for i := range cfg.MCP.Servers {
				if cfg.MCP.Servers[i].Name == name {
					serverCfg = &cfg.MCP.Servers[i]
					break
				}
			}
			if serverCfg == nil {
				return fmt.Errorf("mcp test: server %q not found in config", name)
			}

			spinner := ui.NewSpinner(os.Stderr, fmt.Sprintf("Connecting to %s...", name))
			spinner.Start()

			client, err := mcp.NewMCPClient(*serverCfg)
			if err != nil {
				spinner.Stop()
				return fmt.Errorf("mcp test: %w", err)
			}
			defer client.Close()

			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Second)
			defer cancel()

			if err := client.Connect(ctx); err != nil {
				spinner.Stop()
				fmt.Fprintln(os.Stderr, ui.FormatError("Connection failed: "+err.Error()))
				return nil
			}

			tools, err := client.ListTools(ctx)
			spinner.Stop()
			if err != nil {
				fmt.Fprintln(os.Stderr, ui.FormatError("Failed to list tools: "+err.Error()))
				return nil
			}

			fmt.Fprintln(os.Stderr, ui.FormatSuccess(fmt.Sprintf("Connected to %s (%d tools)", name, len(tools))))

			if len(tools) > 0 {
				headers := []string{"TOOL", "DESCRIPTION"}
				var rows [][]string
				for _, t := range tools {
					desc := t.Description
					if len(desc) > 60 {
						desc = desc[:60] + "..."
					}
					rows = append(rows, []string{t.Name, desc})
				}
				fmt.Fprintln(os.Stdout, ui.RenderTable(headers, rows, -1))
			}

			return nil
		},
	}
}
