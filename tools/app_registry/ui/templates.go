package main

import (
	_ "embed"
)

//go:embed ../wireframe/themes.css
var themesCSS string

const themeCSS = `<style type="text/css">` + "\n" + `/* daisyUI theme override — loaded AFTER daisyUI CDN. */` + "\n\n" + themesCSS + `</style>`
