package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	faktory "github.com/sderosiaux/faktory"
	"github.com/spf13/cobra"
)

func newMemory() (*faktory.Memory, error) {
	return faktory.New(faktory.LoadConfig())
}

func main() {
	root := &cobra.Command{
		Use:   "faktory",
		Short: "Opinionated fact memory store for AI agents",
	}

	var user string
	root.PersistentFlags().StringVar(&user, "user", "", "User ID (required)")

	// --- add ---
	addCmd := &cobra.Command{
		Use:   "add [message...]",
		Short: "Extract and store facts from a conversation",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			messages := []faktory.Message{
				{Role: "user", Content: strings.Join(args, " ")},
			}

			result, err := mem.Add(context.Background(), messages, user)
			if err != nil {
				return err
			}

			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}

	// --- search ---
	var searchLimit int
	searchCmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search facts by semantic similarity",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			facts, err := mem.Search(context.Background(), strings.Join(args, " "), user, searchLimit)
			if err != nil {
				return err
			}

			for _, f := range facts {
				fmt.Printf("  [%.2f] %s\n", f.Score, f.Text)
			}
			if len(facts) == 0 {
				fmt.Println("  (no results)")
			}
			return nil
		},
	}
	searchCmd.Flags().IntVar(&searchLimit, "limit", 10, "Max results")

	// --- facts ---
	var factsLimit int
	factsCmd := &cobra.Command{
		Use:   "facts",
		Short: "List all facts for a user",
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			facts, err := mem.GetAll(context.Background(), user, factsLimit)
			if err != nil {
				return err
			}

			for _, f := range facts {
				fmt.Printf("  %s\n", f.Text)
			}
			if len(facts) == 0 {
				fmt.Println("  (no facts)")
			}
			return nil
		},
	}
	factsCmd.Flags().IntVar(&factsLimit, "limit", 100, "Max results")

	// --- relations ---
	var relLimit int
	relCmd := &cobra.Command{
		Use:   "relations",
		Short: "List all entity relations for a user",
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			rels, err := mem.GetAllRelations(context.Background(), user, relLimit)
			if err != nil {
				return err
			}

			for _, r := range rels {
				fmt.Printf("  %s --%s--> %s\n", r.Source, r.Relation, r.Target)
			}
			if len(rels) == 0 {
				fmt.Println("  (no relations)")
			}
			return nil
		},
	}
	relCmd.Flags().IntVar(&relLimit, "limit", 100, "Max results")

	// --- delete ---
	deleteCmd := &cobra.Command{
		Use:   "delete [fact-id]",
		Short: "Delete a specific fact",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			if err := mem.Delete(context.Background(), args[0]); err != nil {
				return err
			}
			fmt.Println("deleted")
			return nil
		},
	}

	// --- delete-all ---
	deleteAllCmd := &cobra.Command{
		Use:   "delete-all",
		Short: "Delete all data for a user",
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			if err := mem.DeleteAll(context.Background(), user); err != nil {
				return err
			}
			fmt.Println("deleted all data for user:", user)
			return nil
		},
	}

	// --- get ---
	getCmd := &cobra.Command{
		Use:   "get [fact-id]",
		Short: "Get a single fact by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			fact, err := mem.Get(context.Background(), args[0])
			if err != nil {
				return err
			}
			if fact == nil {
				fmt.Println("not found")
				return nil
			}
			out, _ := json.MarshalIndent(fact, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}

	// --- update ---
	updateCmd := &cobra.Command{
		Use:   "update [fact-id] [new-text...]",
		Short: "Update a fact's text",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			text := strings.Join(args[1:], " ")
			if err := mem.Update(context.Background(), args[0], text); err != nil {
				return err
			}
			fmt.Println("updated")
			return nil
		},
	}

	// --- recall ---
	var recallMaxFacts, recallMaxRels int
	var recallProfile bool
	recallCmd := &cobra.Command{
		Use:   "recall [query]",
		Short: "Retrieve relevant facts and relations in one call",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			opts := &faktory.RecallOptions{
				MaxFacts:       recallMaxFacts,
				MaxRelations:   recallMaxRels,
				IncludeProfile: recallProfile,
			}
			result, err := mem.Recall(context.Background(), strings.Join(args, " "), user, opts)
			if err != nil {
				return err
			}

			if result.Summary != "" {
				fmt.Print(result.Summary)
			} else {
				fmt.Println("(no relevant memories)")
			}
			return nil
		},
	}
	recallCmd.Flags().IntVar(&recallMaxFacts, "max-facts", 10, "Max facts to return")
	recallCmd.Flags().IntVar(&recallMaxRels, "max-relations", 10, "Max relations to return")
	recallCmd.Flags().BoolVar(&recallProfile, "profile", false, "Include generated user profile in output")

	// --- profile ---
	profileCmd := &cobra.Command{
		Use:   "profile",
		Short: "Generate a user profile summary from stored facts",
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			profile, err := mem.Profile(context.Background(), user)
			if err != nil {
				return err
			}
			if profile == "" {
				fmt.Println("(no facts stored for this user)")
				return nil
			}
			fmt.Println(profile)
			return nil
		},
	}

	// --- export ---
	exportCmd := &cobra.Command{
		Use:   "export",
		Short: "Export all data for a user as JSONL",
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			return mem.Export(context.Background(), user, os.Stdout)
		},
	}

	// --- import ---
	importCmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Import JSONL data for a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if user == "" {
				return fmt.Errorf("--user is required")
			}
			mem, err := newMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			f, err := os.Open(args[0])
			if err != nil {
				return fmt.Errorf("open file: %w", err)
			}
			defer f.Close()

			if err := mem.Import(context.Background(), user, f); err != nil {
				return err
			}
			fmt.Println("import complete")
			return nil
		},
	}

	root.AddCommand(addCmd, searchCmd, factsCmd, relCmd, deleteCmd, deleteAllCmd, getCmd, updateCmd, recallCmd, exportCmd, importCmd, profileCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
