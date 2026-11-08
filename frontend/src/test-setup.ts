import "@testing-library/jest-dom";
import { configure } from "@testing-library/react";

// findBy* and waitFor give up after 1 s by default, which a busy machine (a parallel run, CI)
// overruns while a test is still correct. A passing wait returns as soon as it succeeds, so only
// a failing test waits longer.
configure({ asyncUtilTimeout: 5000 });
