# Hearthport brand assets

![Hearthport banner](github-readme-banner.png)

The doorway and hearth flame are original vector artwork. Colours: deep navy `#142531`, ember `#EAB377`, warm ivory `#FFF6E8`, sea glass `#8FC8B0`. Wordmark text is rendered in DejaVu Sans Bold; the GitHub banner and wordmark SVGs retain editable text.

## Use

- **GitHub README:** `![Hearthport](assets/github-readme-banner.png)` after copying assets into an `assets/` folder.
- **Favicon:** copy `favicon.ico` (contains 16, 32 and 48 px), `favicon.svg` and `icon-32.png` into your public directory.
- **PWA:** put `icon-192.png`, `icon-512.png`, and both `icon-maskable-*.png` files under `/icons/`. Copy `site.webmanifest` to the site root; adjust `start_url` and icon paths if your deployment uses a subpath.
- **Apple web clip:** use `apple-touch-icon.png` (180×180). **Apple App Store/icon composer source:** use `apple-app-icon-1024.png` (opaque, 1024×1024).
- **Source:** `hearthport-mark.svg`, `hearthport-wordmark.svg`, `github-readme-banner.svg`. The transparent mark works on a dark surface; use `hearthport-mark-on-dark.svg` when a background is required. On light surfaces (light mode, white pages) use `hearthport-mark-on-light.svg`, where the ivory inner arch and flame highlight become navy so they stay visible. `hearthport-mark-mono.svg` is a single-colour version.

```html
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<link rel="icon" type="image/x-icon" href="/favicon.ico">
<link rel="apple-touch-icon" href="/apple-touch-icon.png">
<link rel="manifest" href="/site.webmanifest">
<meta name="theme-color" content="#142531">
```

The master SVG mark can be recoloured and scaled. Raster icons are generated at 16, 32, 48, 64, 96, 128, 180, 192, 256, 512 and 1024 pixels. Maskable icons have extra padding. No third-party franchise art is used.

The wordmark and banner SVGs use live text (DejaVu Sans). On machines without that font the text falls back to Arial, so use the PNGs, or convert the text to outlines before using the SVGs in the app.
