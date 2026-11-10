import {create, toBinary} from "@bufbuild/protobuf";
import {CloseReason, ErrorCode, PlayholdClient, type ClientStatus} from "playhold-sdk";

import {HandsMessage, PlaySchema, SchemaVersion} from "./gen/cards/v1/cards_pb";
import {
    cardLabel,
    isRed,
    isWild,
    otherPlayerAway,
    readViews,
    suits,
    suitSymbol,
    timerBar,
    turnText,
    type GameView,
} from "./view";

// The rules stay on the server: this page shows what the server sent, and offers the moves it said are possible.

const statusText: Record<ClientStatus, string> = {
    disconnected: "Connection lost. Reconnecting…",
    connecting: "Connecting…",
    connected: "",
    joined: "",
    replaced: "You opened this game somewhere else, so it stopped here.",
    closed: "Disconnected.",
    updateRequired: "There's a newer version of this game. Reload to keep playing.",
};

function element<T extends HTMLElement>(id: string): T {
    const found = document.getElementById(id);
    if (!found) {
        throw new Error(`missing element: ${id}`);
    }
    return found as T;
}

const ui = {
    status: element("status"),
    message: element("message"),
    reclaim: element<HTMLButtonElement>("reclaim"),
    reload: element<HTMLButtonElement>("reload"),
    lobby: element("lobby"),
    newGame: element<HTMLButtonElement>("new-game"),
    joinForm: element<HTMLFormElement>("join-form"),
    code: element<HTMLInputElement>("code"),
    game: element("game"),
    room: element("room"),
    turn: element("turn"),
    away: element("away"),
    timer: element("timer"),
    timerFill: element("timer-fill"),
    timerText: element("timer-text"),
    top: element<HTMLButtonElement>("top"),
    table: element("table"),
    hand: element("hand"),
    suits: element("suits"),
    draw: element<HTMLButtonElement>("draw"),
    pass: element<HTMLButtonElement>("pass"),
};

/** The server to connect to: set at build time for a page hosted elsewhere, otherwise the page's own server. */
function serverUrl(): string {
    const configured: unknown = import.meta.env.VITE_SERVER_URL;
    if (typeof configured === "string" && configured !== "") {
        return configured;
    }
    const url = new URL("/ws", location.href);
    url.protocol = location.protocol === "https:" ? "wss:" : "ws:";
    return url.href;
}

let game: GameView = {};
let wild: string | undefined; // the eight waiting for a suit
let turnEndsAt = 0; // on this browser's clock, worked out from the time left, so the two clocks needn't agree
let turnTotalMs = 0;
let timerFrozen = false;

const client = new PlayholdClient({
    url: serverUrl(),
    schemaVersions: {turns: SchemaVersion.CURRENT},
    onStatus: (status) => {
        ui.status.textContent = statusText[status];
        ui.reclaim.hidden = status !== "replaced";
        ui.reload.hidden = status !== "updateRequired";
        if (status !== "joined") {
            timerFrozen = true; // the server's count may be paused, so don't guess: the next state unfreezes it
        }
        if (status === "joined") {
            ui.message.textContent = ""; // anything said while getting here no longer applies
            ui.lobby.hidden = true;
            ui.game.hidden = false;
        }
    },
    onState: (views) => {
        game = readViews(views);
        turnEndsAt = performance.now() + Number(game.turns?.turnMillisLeft ?? 0n);
        turnTotalMs = Number(game.turns?.turnMillisTotal ?? 0n);
        timerFrozen = false;
        render();
    },
    onError: (error) => {
        ui.message.textContent = error.message;
        // The room refused this player, unless the server only asked them to try again, which the client is about to do.
        if (client.status === "connected" && error.code !== ErrorCode.UNAVAILABLE) {
            showLobby();
        }
    },
    onRoomClosed: (reason) => {
        ui.message.textContent =
            reason === CloseReason.REQUESTED ? "The room closed." : "The room closed because of a server problem.";
        showLobby();
    },
    onJoined: (code) => showRoom(code),
});

/** Shows an empty game while the server answers. */
function openGame(): void {
    ui.message.textContent = "";
    ui.lobby.hidden = true;
    ui.game.hidden = false;
    game = {};
    render();
}

/** Shows the room's code, and puts it in the page's address, for the other player to follow. */
function showRoom(code: string): void {
    ui.room.textContent = code;
    const url = new URL(location.href);
    url.searchParams.set("room", code);
    history.replaceState(null, "", url);
}

function join(code: string): void {
    openGame();
    showRoom(code);
    client.join(code);
}

/** Asks the server for a new room. Its code arrives in onJoined. */
function createRoom(): void {
    openGame();
    ui.room.textContent = "…";
    client.createRoom();
}

function showLobby(): void {
    ui.lobby.hidden = false;
    ui.game.hidden = true;
}

function play(card: string, chosenSuit = ""): void {
    wild = undefined;
    ui.message.textContent = "";
    client.send("hands", HandsMessage.PLAY, toBinary(PlaySchema, create(PlaySchema, {card, chosenSuit})));
    render();
}

/** Moves the turn timer along. A state from the server resets it to the true time left. */
function renderTimer(): void {
    ui.timer.hidden = turnTotalMs === 0;
    if (turnTotalMs === 0 || timerFrozen) {
        return;
    }
    const bar = timerBar(turnEndsAt - performance.now(), turnTotalMs);
    ui.timerFill.style.width = bar.width;
    ui.timerFill.style.background = bar.color;
    ui.timerText.textContent = bar.text;
}

function render(): void {
    renderTimer();
    const {deck, turns, hands} = game;
    ui.turn.textContent = turnText(turns, client.playerId);
    ui.away.hidden = !otherPlayerAway(turns, client.playerId);

    const top = deck?.top ?? "";
    ui.top.textContent = top === "" ? "–" : cardLabel(top);
    ui.top.classList.toggle("red", top !== "" && isRed(top));
    ui.table.textContent = deck
        ? `Suit in play: ${suitSymbol(deck.activeSuit)}. Cards to draw: ${deck.drawCount}. ` +
          `They hold: ${hands?.others[0]?.count ?? 0}.`
        : "";

    const playable = hands?.playable ?? [];
    ui.hand.replaceChildren(
        ...(hands?.cards ?? []).map((card) => {
            const button = document.createElement("button");
            button.className = isRed(card) ? "card red" : "card";
            button.textContent = cardLabel(card);
            button.disabled = !playable.includes(card);
            button.addEventListener("click", () => {
                if (isWild(card)) {
                    wild = card;
                    render();
                } else {
                    play(card);
                }
            });
            return button;
        }),
    );

    ui.suits.hidden = wild === undefined;
    ui.suits.replaceChildren(
        ...suits.map((suit) => {
            const button = document.createElement("button");
            button.textContent = `Continue with ${suitSymbol(suit)}`;
            button.addEventListener("click", () => {
                if (wild !== undefined) {
                    play(wild, suit);
                }
            });
            return button;
        }),
    );

    ui.draw.disabled = !hands?.canDraw;
    ui.pass.disabled = !hands?.canPass;
}

ui.newGame.addEventListener("click", createRoom);
ui.joinForm.addEventListener("submit", (event) => {
    event.preventDefault();
    join(ui.code.value.trim());
});
ui.draw.addEventListener("click", () => {
    ui.message.textContent = "";
    client.send("hands", HandsMessage.DRAW, new Uint8Array());
});
ui.pass.addEventListener("click", () => {
    ui.message.textContent = "";
    client.send("hands", HandsMessage.PASS, new Uint8Array());
});
ui.reclaim.addEventListener("click", () => client.connect());
ui.reload.addEventListener("click", () => location.reload());

setInterval(renderTimer, 100);

client.connect();
const shared = new URLSearchParams(location.search).get("room");
if (shared) {
    join(shared);
}