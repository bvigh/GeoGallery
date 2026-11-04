import plugin from "tailwindcss/plugin";

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: ["class"],
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      screens: {
        // Phones held sideways: wide enough for sm/md (≥ 568px), but only ~320–430px tall.
        // The min-width keeps out a portrait phone (≤ ~430px wide) with the on-screen keyboard
        // open: interactive-widget=resizes-content (index.html) leaves it ~380–490px tall, and its
        // bars must not switch to the sideways row layouts. Not (orientation: landscape): with the
        // keyboard open a portrait phone can be wider than tall (412×380).
        // A raw screen turns off Tailwind's max-*, min-[…] and max-[…] screen variants for the
        // whole project: named ones like max-sm: produce no CSS and no warning. Use the
        // default-hidden + sm:… pattern instead. Keep short a screen: as a plugin variant it would
        // be emitted before sm:/md:/lg: and lose to them. tailwind.config.test.ts pins both.
        short: { raw: "(max-height: 500px) and (min-width: 560px)" },
      },
      colors: {
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        primary: {
          DEFAULT: "hsl(var(--primary))",
          foreground: "hsl(var(--primary-foreground))",
        },
        secondary: {
          DEFAULT: "hsl(var(--secondary))",
          foreground: "hsl(var(--secondary-foreground))",
        },
        muted: {
          DEFAULT: "hsl(var(--muted))",
          foreground: "hsl(var(--muted-foreground))",
        },
        accent: {
          DEFAULT: "hsl(var(--accent))",
          foreground: "hsl(var(--accent-foreground))",
        },
        destructive: {
          DEFAULT: "hsl(var(--destructive))",
          foreground: "hsl(var(--destructive-foreground))",
        },
        card: {
          DEFAULT: "hsl(var(--card))",
          foreground: "hsl(var(--card-foreground))",
        },
      },
      borderRadius: {
        lg: "var(--radius)",
        md: "calc(var(--radius) - 2px)",
        sm: "calc(var(--radius) - 4px)",
      },
    },
  },
  plugins: [
    // touch: the primary pointer is a finger (phones, tablets); mouse: a precise, hovering pointer.
    // A laptop with a touchscreen still matches mouse.
    plugin(({ addVariant }) => {
      addVariant("touch", "@media (pointer: coarse)");
      addVariant("mouse", "@media (hover: hover) and (pointer: fine)");
    }),
  ],
};
