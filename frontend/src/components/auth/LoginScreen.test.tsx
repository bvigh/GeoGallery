import { render, screen } from "@testing-library/react";
import { describe, it, expect, afterEach } from "vitest";
import { LoginScreen } from "@/components/auth/LoginScreen";

function setSearch(search: string) {
  window.history.replaceState(null, "", "/" + search);
}

describe("LoginScreen", () => {
  afterEach(() => setSearch(""));

  it("links Sign in to the login endpoint without an error message", () => {
    render(<LoginScreen />);
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/api/auth/login");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Sign in with a different account" })).not.toBeInTheDocument();
  });

  it("links the privacy policy and terms of service", () => {
    render(<LoginScreen />);
    expect(screen.getByRole("link", { name: "Privacy Policy" })).toHaveAttribute("href", "/privacy.html");
    expect(screen.getByRole("link", { name: "Terms of Service" })).toHaveAttribute("href", "/terms.html");
  });

  it.each([
    ["not_invited", "This account isn't invited"],
    ["invite_invalid", "This invite link is invalid"],
    ["disabled", "This account has been disabled"],
  ])("shows a message for auth_error=%s", (code, text) => {
    setSearch(`?auth_error=${code}`);
    render(<LoginScreen />);
    expect(screen.getByRole("alert")).toHaveTextContent(text);
    expect(screen.getByRole("link", { name: "Sign in with a different account" })).toHaveAttribute(
      "href",
      "/api/auth/login?prompt=login"
    );
  });

  it("forwards the invite to both buttons", () => {
    setSearch("?invite=abc123&auth_error=not_invited");
    render(<LoginScreen />);
    expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/api/auth/login?invite=abc123");
    expect(screen.getByRole("link", { name: "Sign in with a different account" })).toHaveAttribute(
      "href",
      "/api/auth/login?prompt=login&invite=abc123"
    );
  });
});
