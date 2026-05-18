# Tailwind CSS + CSS Fundamentals — Selected Documentation

Source: context7 `/tailwindlabs/tailwindcss.com`

## Gap Utility Classes (Flex / Grid)

Utilities for controlling gutters between grid and flexbox items.

```html
<!-- Uniform gap -->
<div class="grid grid-cols-2 gap-4">
  <div>01</div>
  <div>02</div>
</div>

<!-- Independent row and column gaps -->
<div class="grid grid-cols-3 gap-x-8 gap-y-4">
  <div>01</div><div>02</div><div>03</div>
</div>

<!-- Arbitrary values -->
<div class="grid gap-[10vw]">…</div>

<!-- Responsive -->
<div class="grid gap-4 md:gap-6">…</div>
```

Variants: `gap-<number>`, `gap-x-<number>`, `gap-y-<number>`, `gap-[<value>]`, `gap-(<custom-property>)`. All support responsive prefixes (`sm:`, `md:`, `lg:`, `xl:`, `2xl:`).

## Grid Template Columns

```css
grid-cols-<number> { grid-template-columns: repeat(<number>, minmax(0, 1fr)); }
grid-cols-none     { grid-template-columns: none; }
grid-cols-subgrid  { grid-template-columns: subgrid; }
grid-cols-[<value>] { grid-template-columns: <value>; }
grid-cols-(<custom-property>) { grid-template-columns: var(<custom-property>); }
```

## Class-Based Dark Mode (Tailwind v4)

Override the default `dark` variant to trigger on `.dark` class instead of `prefers-color-scheme`.

```css
@import "tailwindcss";

@custom-variant dark (&:where(.dark, .dark *));
```

```html
<html class="dark">
  <body>
    <div class="bg-white dark:bg-black">…</div>
  </body>
</html>
```

## Common Utility Class Families

- **Spacing**: `p-*`, `px-*`, `py-*`, `pt/r/b/l-*`, `m-*`, `mx-*`, `my-*`, `mt/r/b/l-*`, `space-x-*`, `space-y-*`.
- **Sizing**: `w-*`, `h-*`, `min-w-*`, `max-w-*`, `min-h-*`, `max-h-*`. Special: `w-screen`, `w-full`, `w-1/2`, `w-fit`.
- **Flexbox**: `flex`, `flex-row|col|wrap`, `items-{start|center|end|baseline|stretch}`, `justify-{start|center|end|between|around|evenly}`, `gap-*`.
- **Grid**: `grid`, `grid-cols-*`, `grid-rows-*`, `col-span-*`, `row-span-*`.
- **Typography**: `text-{xs|sm|base|lg|xl|2xl...}`, `font-{thin|light|normal|medium|semibold|bold|extrabold}`, `leading-*`, `tracking-*`, `text-{color}-{shade}`.
- **Background**: `bg-{color}-{shade}`, `bg-gradient-to-r`, `from-*`, `via-*`, `to-*`.
- **Border**: `border`, `border-{n}`, `border-{color}-{shade}`, `rounded-{none|sm|md|lg|xl|full}`.
- **Effects**: `shadow-{sm|md|lg|xl|2xl}`, `opacity-*`, `transition`, `duration-*`, `ease-{linear|in|out|in-out}`.
- **Interactivity**: `cursor-pointer`, `select-none`, `pointer-events-none`, `hover:*`, `focus:*`, `active:*`, `disabled:*`.

## Responsive Variants

Tailwind is mobile-first; variants apply from the named breakpoint UP.

```html
<div class="text-sm md:text-base lg:text-lg">Responsive text</div>
```

Default breakpoints: `sm` 640px, `md` 768px, `lg` 1024px, `xl` 1280px, `2xl` 1536px.

## State Variants

- `hover:`, `focus:`, `focus-visible:`, `active:`, `visited:`, `disabled:`
- `group-hover:`, `peer-checked:`
- `aria-*:`, `data-*:`
- `first:`, `last:`, `odd:`, `even:`
- `not-first:`, `[&:nth-child(3)]:` (arbitrary variants)

## Arbitrary Values

When a utility doesn't exist for a specific value:

```html
<div class="top-[117px] grid-cols-[1fr_2fr_1fr] text-[#1da1f2]">
  Custom values inline.
</div>
```

## CSS Fundamentals (Generic — for non-Tailwind contexts)

- **Box model**: `box-sizing: border-box` makes width/height include padding+border.
- **Display**: `block`, `inline`, `inline-block`, `flex`, `grid`, `none`.
- **Position**: `static`, `relative`, `absolute`, `fixed`, `sticky`. Sticky requires a scrollable ancestor.
- **Flexbox**: `display: flex` on parent; `flex-direction: row|column`; `justify-content` (main axis), `align-items` (cross axis), `gap`.
- **Grid**: `display: grid`; `grid-template-columns: repeat(3, 1fr)`; `gap`; `grid-area`; named areas via `grid-template-areas`.
- **Custom properties**: `--brand: #007aff;` then `color: var(--brand);`. Inheritable, themable.
- **Cascade layers (`@layer`)**: control specificity across imported sheets.
- **Container queries**: `@container (min-width: 400px) { ... }` after setting `container-type: inline-size`.
- **Logical properties**: `padding-inline`, `margin-block`, `border-inline-start` — direction-aware for LTR/RTL.

## Best Practices

- Prefer utility classes over component-level abstractions until duplication actually appears.
- Extract repeated utility groups with `@apply` only when truly stable; otherwise keep them inline for grep-ability.
- Use Tailwind's design tokens (`text-gray-900`) over raw hex codes; redefine palette in `@theme` if needed.
- Keep `dark:` variants colocated with light defaults; don't duplicate trees.
- Lazy-load Tailwind via `@import "tailwindcss"` in CSS (v4) or via PostCSS plugin (v3).
- Run the JIT engine in dev to keep bundles tiny.
