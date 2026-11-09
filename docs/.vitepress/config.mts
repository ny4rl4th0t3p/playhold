import {defineConfig} from "vitepress";

export default defineConfig({
    title: "playhold",
    description: "A self-hosted game backend in Go, built from authoritative rooms and composable modules.",
    base: "/playhold/",
    themeConfig: {
        nav: [
            {text: "Guides", link: "/guides/production"},
            {text: "Reference", link: "/reference/configuration"},
            {text: "Changelog", link: "/changelog"},
        ],
        sidebar: [
            {
                text: "Guides",
                items: [
                    {text: "Run in production", link: "/guides/production"},
                    {text: "Limit abuse and load", link: "/guides/limits"},
                    {text: "Keep games running", link: "/guides/keeping-games-running"},
                    {text: "When something crashes", link: "/guides/crashes"},
                    {text: "Watch the server", link: "/guides/observability"},
                ],
            },
            {
                text: "Reference",
                items: [
                    {text: "Configuration", link: "/reference/configuration"},
                    {text: "Errors and close reasons", link: "/reference/errors"},
                ],
            },
            {text: "Measurements", link: "/measurements"},
            {text: "Changelog", link: "/changelog"},
        ],
        socialLinks: [{icon: "github", link: "https://github.com/ny4rl4th0t3p/playhold"}],
        search: {provider: "local"},
    },
});