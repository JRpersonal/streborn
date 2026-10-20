// The display font generator is its own module so its rasteriser
// (golang.org/x/image) never becomes a dependency of the agent: the agent
// links only the tables it writes, not the code that made them.
module github.com/JRpersonal/streborn/internal/oled/fontgen

go 1.26.0

require golang.org/x/image v0.47.0

require (
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)
