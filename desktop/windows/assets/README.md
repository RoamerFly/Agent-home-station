# Desktop application icon

The selected design is a general conversational Agent with modular plugins:
one blue chat bubble containing three interlocking rounded modules, without a
person or animal mascot. Generated with the built-in gpt-image tool on
2026-10-30. Earlier animal/character candidates are not used by the project.

`agent-chat-plugins.png` is the original transparent generated design.
`agent-chat-plugins.ico` packages it at 16, 20, 24, 32, 40, 48, 64, 96, 128,
and 256 pixels. Rebuild the ICO using `python scripts/build_windows_icon.py`
(Pillow is a build-time dependency). The EXE, window and tray use this icon.

Generation prompt:

> Use case: logo-brand. Asset type: production Windows desktop app icon for a general-purpose multi-plugin conversational AI Agent, NOT an animal or humanoid mascot. Create ONE very simple bold recognizable geometric symbol: a rounded square chat bubble with a clear speech tail, and inside it a compact interlocking 3-piece modular/plugin motif, like linked rounded blocks fitting together. Cohesive original symbol, not a collection of separate floating icons. Professional software utility aesthetic, cobalt blue and cyan with one small soft white connecting element, subtle premium dimensional shading, strong outline separation, no fine details. The symbol should be instantly readable at 16px and 32px. Centered on transparent background with about 10% empty transparent padding all around, square canvas. No letters, no words, no numbers, no watermark, no robot face, no eyes, no anime, no people, no animals, no gears, no circuitry, no background tile, no mockup. Crisp clean anti-aliased edges, high resolution app icon ready for conversion to Windows ICO.
