import {create, toBinary} from "@bufbuild/protobuf";
import {describe, expect, it} from "vitest";

import {DeckViewSchema, HandsViewSchema, TurnsViewSchema} from "./gen/cards/v1/cards_pb";
import {cardLabel, isRed, isWild, readViews, suitSymbol, timerBar, turnText} from "./view";

describe("readViews", () => {
    it("decodes each module's view, and ignores unknown modules", () => {
        const game = readViews([
            {
                $typeName: "playhold.v1.ModuleView",
                module: "deck",
                view: toBinary(DeckViewSchema, create(DeckViewSchema, {top: "Q-of-spades"})),
            },
            {
                $typeName: "playhold.v1.ModuleView",
                module: "hands",
                view: toBinary(HandsViewSchema, create(HandsViewSchema, {cards: ["3-of-clubs"]})),
            },
            {$typeName: "playhold.v1.ModuleView", module: "other", view: new Uint8Array([1, 2, 3])},
        ]);

        expect(game.deck?.top).toBe("Q-of-spades");
        expect(game.hands?.cards).toEqual(["3-of-clubs"]);
        expect(game.turns).toBeUndefined();
    });
});

describe("cards", () => {
    it.each([
        ["Q-of-spades", "Q♠", false, false],
        ["10-of-hearts", "10♥", true, false],
        ["8-of-diamonds", "8♦", true, true],
        ["A-of-clubs", "A♣", false, false],
    ])("%s", (card, label, red, wild) => {
        expect(cardLabel(card)).toBe(label);
        expect(isRed(card)).toBe(red);
        expect(isWild(card)).toBe(wild);
    });

    it("shows an unknown suit as it is", () => {
        expect(suitSymbol("stars")).toBe("stars");
    });
});

describe("timerBar", () => {
    it.each([
        ["full", 60_000, "100.0%", "hsl(120 70% 45%)", "60 s"],
        ["half", 30_000, "50.0%", "hsl(60 70% 45%)", "30 s"],
        ["nearly out", 500, "0.8%", "hsl(1 70% 45%)", "1 s"],
        ["out", 0, "0.0%", "hsl(0 70% 45%)", "0 s"],
        ["overdue", -5_000, "0.0%", "hsl(0 70% 45%)", "0 s"],
        ["more than the total", 90_000, "100.0%", "hsl(120 70% 45%)", "90 s"],
    ])("%s", (_, left, width, color, text) => {
        expect(timerBar(left, 60_000)).toEqual({width, color, text});
    });

    it("copes with no total", () => {
        expect(timerBar(1_000, 0).width).toBe("0.0%");
    });
});

describe("turnText", () => {
    const turns = (fields: {started?: boolean; current?: string; winner?: string}) =>
        create(TurnsViewSchema, {started: true, ...fields});

    it.each([
        ["before the game", undefined, "Waiting for another player. Share the room code."],
        ["not started", turns({started: false}), "Waiting for another player. Share the room code."],
        ["my turn", turns({current: "me"}), "Your turn."],
        ["their turn", turns({current: "them"}), "Their turn."],
        ["I won", turns({winner: "me"}), "You won!"],
        ["they won", turns({winner: "them"}), "You lost."],
    ])("%s", (_, view, text) => {
        expect(turnText(view, "me")).toBe(text);
    });
});