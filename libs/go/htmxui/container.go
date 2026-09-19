package htmxui

// ContainerWide is the shared "wide screen" ShellData.ContainerClass preset
// for information-dense pages -- dashboards, multi-column detail views --
// that need more horizontal room than the default max-w-4xl once the
// viewport is large enough, while staying at the default narrow width on
// small/medium screens where the extra columns would just wrap. Pass it as
// ShellData.ContainerClass to opt a page into this preset instead of
// hand-writing the breakpoint classes at each call site (audience_score_
// system's Idea detail page -- research.IdeaDetail -- did that
// independently before this constant existed).
const ContainerWide = "max-w-4xl xl:max-w-7xl 2xl:max-w-[1600px]"
