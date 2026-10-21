package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/teknoir/cli/pkg/api"
)

func newConfigKindsCmd(e *cliEnv) *cobra.Command {
	var template string
	c := &cobra.Command{
		SilenceUsage: true,
		Use:          "kinds [--template KIND]",
		Short:        "List the kinds 'config add' accepts as local resources, and the ones it refuses",
		Long: `List the Kubernetes kinds that can be added to a robot with 'tnctl config add', with their scope and
a note, and the kinds that are refused with the reason. Everything else, such as a custom resource, is refused
too: there is no schema to check it with, and a custom resource whose CRD is missing on the robot stops the
robot's whole configuration from applying.

  --template KIND   print a starter manifest of a kind (YAML), to save and edit:
                    tnctl config kinds --template Deployment > whoami.yaml

-o json prints the platform's answer, which includes the templates.`,
		Example: `  tnctl config kinds
  tnctl config kinds --template Deployment > whoami.yaml
  tnctl config add vision-station-01 -f whoami.yaml --dry-run
  tnctl config kinds -o json`,
		Args: argsRange(0, 0, "kinds takes no arguments"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := outputFormat()
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true
			fl, _, _, err := e.targetClient()
			if err != nil {
				return err
			}
			cat, raw, err := fl.LocalKinds(cmd.Context())
			if err != nil {
				return classify(err)
			}
			out := cmd.OutOrStdout()
			if template != "" {
				return printKindTemplate(out, cat, template)
			}
			if format != "table" {
				return writeStructured(out, format, raw)
			}
			printKinds(out, cat)
			return nil
		},
	}
	c.Flags().StringVar(&template, "template", "", "print a starter manifest of this kind")
	return c
}

func printKinds(out io.Writer, cat *api.LocalKindCatalog) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "APIVERSION\tKIND\tSCOPE\tNOTE")
	for _, k := range cat.Kinds {
		scope := "namespaced"
		if !k.Namespaced {
			scope = "cluster"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.APIVersion, k.Kind, scope, k.Note)
	}
	_ = tw.Flush()
	if len(cat.Refused) > 0 {
		fmt.Fprintln(out, "\nRefused:")
		tw = tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "KIND\tAPIVERSION\tREASON")
		for _, r := range cat.Refused {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Kind, r.APIVersion, r.Reason)
		}
		_ = tw.Flush()
	}
	if cat.SchemaSource != "" {
		fmt.Fprintf(out, "\nChecked against %s.\n", cat.SchemaSource)
	}
}

// printKindTemplate prints the starter manifest of the kind (matched without regard to case).
func printKindTemplate(out io.Writer, cat *api.LocalKindCatalog, kind string) error {
	var found []api.LocalKindInfo
	for _, k := range cat.Kinds {
		if strings.EqualFold(k.Kind, kind) {
			found = append(found, k)
		}
	}
	switch len(found) {
	case 0:
		var names []string
		for _, k := range cat.Kinds {
			names = append(names, k.Kind)
		}
		for _, r := range cat.Refused {
			if strings.EqualFold(r.Kind, kind) {
				return newErr(codeUsage, "%s cannot be added: %s", r.Kind, r.Reason)
			}
		}
		return usageErr("no supported kind %q; the kinds are %s", kind, strings.Join(names, ", "))
	case 1:
		t := found[0].Template
		if t == "" {
			return newErr(codeNotFound, "the platform has no template for %s", found[0].Kind)
		}
		if !strings.HasSuffix(t, "\n") {
			t += "\n"
		}
		_, err := fmt.Fprint(out, t)
		return err
	}
	return usageErr("%q matches several kinds (%s %s)", kind, found[0].APIVersion, found[1].APIVersion)
}
