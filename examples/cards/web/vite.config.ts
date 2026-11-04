import {defineConfig} from "vitest/config";

export default defineConfig({
    // Relative paths, so the build also works from a subfolder such as an itch.io upload.
    base: "./",
    test: {
        include: ["src/**/*.test.ts"],
    },
});