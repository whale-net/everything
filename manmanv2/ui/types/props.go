package types

// ButtonVariant defines button style variants
type ButtonVariant int

const (
	ButtonPrimary ButtonVariant = iota
	ButtonSecondary
	ButtonDanger
	ButtonSuccess
)

// ButtonSize defines button sizes
type ButtonSize int

const (
	ButtonSizeSmall ButtonSize = iota
	ButtonSizeMedium
	ButtonSizeLarge
)

// ButtonProps configures button components
type ButtonProps struct {
	Variant  ButtonVariant
	Size     ButtonSize
	Class    string
	Disabled bool
	Type     string // "button", "submit", "reset"
}

// BadgeVariant defines badge style variants
type BadgeVariant int

const (
	BadgePrimary BadgeVariant = iota
	BadgeSecondary
	BadgeDanger
	BadgeSuccess
	BadgeWarning
	BadgeInfo
)

// BadgeProps configures badge components
type BadgeProps struct {
	Variant BadgeVariant
	Class   string
}

// CardProps configures card components
type CardProps struct {
	Class string
}

// AlertVariant defines alert style variants
type AlertVariant int

const (
	AlertInfo AlertVariant = iota
	AlertSuccess
	AlertWarning
	AlertDanger
)

// AlertProps configures alert components
type AlertProps struct {
	Variant     AlertVariant
	Class       string
	Dismissible bool
}
