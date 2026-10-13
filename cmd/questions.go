package cmd

import (
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

func newQuestionsCommand() *cobra.Command {
	var opts routingOptions
	c := &cobra.Command{
		Use:   "questions",
		Short: "Print the questions sent to Jev as YAML",
		Long: `Print the wording reflex sends to Jev: the built-in defaults, or the
--questions-file merged over them. Save the output as a starting point for
your own file:

  reflex questions > questions.yaml

Fields:

  context                 optional description of your environment, added to the state
  route.instructions      the "page now or informational?" question
  route.page_now          what deserves a page
  route.informational     what can wait
  urgency.enabled         ask for a logged-only urgency score (default true)
  urgency.instructions    the urgency question
  urgency.levels          2 to 10 levels, least to most urgent

Empty or missing fields use the defaults.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q, err := opts.loadQuestions()
			if err != nil {
				return err
			}
			enc := yaml.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent(2)
			if err := enc.Encode(q); err != nil {
				return err
			}
			return enc.Close()
		},
	}
	c.Flags().StringVar(&opts.questionsFile, "questions-file", "", "YAML questions file to merge over the defaults")
	describeEnv(c)
	return c
}
