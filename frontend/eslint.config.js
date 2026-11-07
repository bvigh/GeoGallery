import js from "@eslint/js";
import { defineConfig, globalIgnores } from "eslint/config";
import reactHooks from "eslint-plugin-react-hooks";
import { reactRefresh } from "eslint-plugin-react-refresh";
import globals from "globals";
import tseslint from "typescript-eslint";

export default defineConfig([
  globalIgnores(["dist", "coverage", "test-results", "playwright-report", "e2e/.auth"]),
  {
    files: ["**/*.{ts,tsx}"],
    extends: [js.configs.recommended, tseslint.configs.recommended, reactRefresh.configs.vite()],
    plugins: { "react-hooks": reactHooks },
    languageOptions: {
      ecmaVersion: 2020,
      globals: { ...globals.browser },
    },
    rules: {
      // The classic hook rules only. The plugin's recommended set also enables the React Compiler
      // rules (refs, set-state-in-effect, immutability, …); the app does not use the compiler, and
      // they flag deliberate patterns such as the latest-value ref (`ref.current = prop`).
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
      // `_name` keeps a parameter the signature needs, like forwardRef's second argument.
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
]);
