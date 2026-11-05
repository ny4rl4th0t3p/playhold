import {fromBinary} from "@bufbuild/protobuf";
import type {ModuleView} from "playhold-sdk";

import {
    DeckViewSchema,
    HandsViewSchema,
    TurnsViewSchema,
    type DeckView,
    type HandsView,
    type TurnsView,
} from "./gen/cards/v1/cards_pb";

export interface GameView {
    deck?: DeckView;
    turns?: TurnsView;
    hands?: HandsView;
}

/** Decodes each module's view, which only that module's message type can read. */
export function readViews(views: ModuleView[]): GameView {
    const game: GameView = {};
    for (const {module, view} of views) {
        switch (module) {
            case "deck":
                game.deck = fromBinary(DeckViewSchema, view);
                break;
            case "turns":
                game.turns = fromBinary(TurnsViewSchema, view);
                break;
            case "hands":
                game.hands = fromBinary(HandsViewSchema, view);
                break;
        }
    }
    return game;
}

export const suits = ["clubs", "diamonds", "hearts", "spades"];

const suitSymbols: Record<string, string> = {clubs: "♣", diamonds: "♦", hearts: "♥", spades: "♠"};

function parts(card: string): {rank: string; suit: string} {
    const [rank = "", suit = ""] = card.split("-of-");
    return {rank, suit};
}

/** Returns a suit's symbol, or the name itself if it isn't a known suit. */
export function suitSymbol(suit: string): string {
    return suitSymbols[suit] ?? suit;
}

/** Returns a short label for a card, such as "Q♠". */
export function cardLabel(card: string): string {
    const {rank, suit} = parts(card);
    return rank + suitSymbol(suit);
}

export function isRed(card: string): boolean {
    const {suit} = parts(card);
    return suit === "hearts" || suit === "diamonds";
}

export function isWild(card: string): boolean {
    return parts(card).rank === "8";
}

/** What the turn timer looks like with this much time left: a bar going from green, through yellow, to red. */
export function timerBar(leftMs: number, totalMs: number): {width: string; color: string; text: string} {
    const fraction = totalMs > 0 ? Math.min(1, Math.max(0, leftMs / totalMs)) : 0;
    const hue = Math.round(fraction * 120); // 120 is green, 60 yellow and 0 red
    return {
        width: `${(fraction * 100).toFixed(1)}%`,
        color: `hsl(${hue} 70% 45%)`,
        text: `${Math.ceil(Math.max(0, leftMs) / 1000)} s`,
    };
}

/** Says whose turn it is, or how the game ended. */
export function turnText(turns: TurnsView | undefined, playerId: string | undefined): string {
    if (!turns?.started) {
        return "Waiting for another player. Share the room code.";
    }
    if (turns.winner !== "") {
        return turns.winner === playerId ? "You won!" : "You lost.";
    }
    return turns.current === playerId ? "Your turn." : "Their turn.";
}