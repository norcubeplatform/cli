package cmdutil

import (
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Command metadata that the tree alone doesn't reveal is declared where
// the command is defined, as cobra annotations. `norcube commands` reads
// them into its machine-readable command tree; nothing else depends on
// them at run time. What the tree does reveal (a --yes, --wait or --cursor
// flag, required flags, aliases, renames) is read from the tree instead
// and needs no annotation.

// Effect says what running a command changes. Every visible command
// declares one (TestEveryCommandDeclaresAnEffect).
type Effect string

const (
	// EffectRead changes nothing on the servers or in the CLI's own state.
	// It may create a new file the user asked for (a download, an export)
	// and refresh caches, but never overwrites anything.
	EffectRead Effect = "read"
	// EffectWrite changes state on a Norcube service (creates, updates,
	// deletes, starts a job, signs in or out). It may change local state
	// as well.
	EffectWrite Effect = "write"
	// EffectLocal changes only local state: the config file, stored
	// credentials, project files or the installed binary.
	EffectLocal Effect = "local"
)

// Annotation keys. Values are part of the `norcube commands` schema, so
// they only ever gain new values.
const (
	// AnnotationEffect holds the command's Effect.
	AnnotationEffect = "norcube/effect"
	// AnnotationService holds the registry key of the service a command
	// (and every command below it) talks to, for commands outside a
	// product group (sign-in, organizations). Product groups don't need
	// it: their name is the registry entry's `command`.
	AnnotationService = "norcube/service"
	// AnnotationNoResult marks a command that prints no result on stdout
	// (it hands the terminal to another program), so -o doesn't apply.
	AnnotationNoResult = "norcube/no-result"
	// AnnotationFlagEnum is the flag annotation listing a flag's allowed
	// values.
	AnnotationFlagEnum = "norcube/enum"
)

// Reads, Writes and Local are the Annotations of a command with that
// Effect: `Annotations: cmdutil.Writes(),` in the command literal.
func Reads() map[string]string  { return effect(EffectRead) }
func Writes() map[string]string { return effect(EffectWrite) }
func Local() map[string]string  { return effect(EffectLocal) }

func effect(e Effect) map[string]string {
	return map[string]string{AnnotationEffect: string(e)}
}

// Annotate adds key=value to cmd's annotations and returns cmd.
func Annotate(cmd *cobra.Command, key, value string) *cobra.Command {
	cmd.Annotations = mergeAnnotations(cmd.Annotations, map[string]string{key: value})
	return cmd
}

// CommandEffect is the Effect cmd declares, or "" when it declares none.
func CommandEffect(cmd *cobra.Command) Effect {
	return Effect(cmd.Annotations[AnnotationEffect])
}

// FlagEnum declares the only values a flag accepts: shell completion
// offers them, and the command tree lists them. The flag must already be
// defined on cmd (as a local or persistent flag); validating the value and
// naming the values in the description stay with the command.
func FlagEnum(cmd *cobra.Command, name string, values ...string) {
	f := cmd.Flags().Lookup(name)
	if f == nil {
		f = cmd.PersistentFlags().Lookup(name)
	}
	if f == nil {
		panic("cmdutil.FlagEnum: no flag --" + name + " on " + cmd.Name())
	}
	if f.Annotations == nil {
		f.Annotations = map[string][]string{}
	}
	f.Annotations[AnnotationFlagEnum] = append([]string(nil), values...)
	if err := cmd.RegisterFlagCompletionFunc(name, cobra.FixedCompletions(values, cobra.ShellCompDirectiveNoFileComp)); err != nil {
		panic("cmdutil.FlagEnum: " + err.Error())
	}
}

// EnumValues are the values FlagEnum recorded for f, or nil.
func EnumValues(f *pflag.Flag) []string {
	return f.Annotations[AnnotationFlagEnum]
}
