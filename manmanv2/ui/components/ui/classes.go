package ui

import (
	"github.com/whale-net/everything/manmanv2/ui/types"
	twmerge "github.com/Oudwins/tailwind-merge-go"
)

// buttonClasses generates Tailwind classes for buttons
func buttonClasses(p types.ButtonProps) string {
	base := "inline-flex items-center justify-center font-medium rounded-md transition-colors focus:outline-none focus:ring-2 focus:ring-offset-2"
	
	// Size classes
	var size string
	switch p.Size {
	case types.ButtonSizeSmall:
		size = "px-3 py-1.5 text-sm"
	case types.ButtonSizeLarge:
		size = "px-6 py-3 text-lg"
	default: // Medium
		size = "px-4 py-2 text-base"
	}
	
	// Variant classes
	var variant string
	switch p.Variant {
	case types.ButtonDanger:
		variant = "bg-red-600 hover:bg-red-700 text-white focus:ring-red-500"
	case types.ButtonSuccess:
		variant = "bg-green-600 hover:bg-green-700 text-white focus:ring-green-500"
	case types.ButtonSecondary:
		variant = "bg-slate-600 hover:bg-slate-700 text-white focus:ring-slate-500"
	default: // Primary
		variant = "bg-indigo-600 hover:bg-indigo-700 text-white focus:ring-indigo-500"
	}
	
	// Disabled state
	disabled := ""
	if p.Disabled {
		disabled = "opacity-50 cursor-not-allowed"
	}
	
	return twmerge.Merge(base, size, variant, disabled, p.Class)
}

// badgeClasses generates Tailwind classes for badges
func badgeClasses(p types.BadgeProps) string {
	base := "inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium"
	
	var variant string
	switch p.Variant {
	case types.BadgeDanger:
		variant = "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200"
	case types.BadgeSuccess:
		variant = "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200"
	case types.BadgeWarning:
		variant = "bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200"
	case types.BadgeInfo:
		variant = "bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200"
	case types.BadgeSecondary:
		variant = "bg-slate-100 text-slate-800 dark:bg-slate-700 dark:text-slate-300"
	default: // Primary
		variant = "bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200"
	}
	
	return twmerge.Merge(base, variant, p.Class)
}

// cardClasses generates Tailwind classes for cards
func cardClasses(p types.CardProps) string {
	base := "bg-white dark:bg-slate-800 rounded-lg shadow-md border border-gray-200 dark:border-slate-700"
	return twmerge.Merge(base, p.Class)
}

// alertClasses generates Tailwind classes for alerts
func alertClasses(p types.AlertProps) string {
	base := "p-4 rounded-lg border"
	
	var variant string
	switch p.Variant {
	case types.AlertSuccess:
		variant = "bg-green-50 border-green-200 text-green-800 dark:bg-green-900/20 dark:border-green-800 dark:text-green-200"
	case types.AlertWarning:
		variant = "bg-yellow-50 border-yellow-200 text-yellow-800 dark:bg-yellow-900/20 dark:border-yellow-800 dark:text-yellow-200"
	case types.AlertDanger:
		variant = "bg-red-50 border-red-200 text-red-800 dark:bg-red-900/20 dark:border-red-800 dark:text-red-200"
	default: // Info
		variant = "bg-blue-50 border-blue-200 text-blue-800 dark:bg-blue-900/20 dark:border-blue-800 dark:text-blue-200"
	}
	
	return twmerge.Merge(base, variant, p.Class)
}
