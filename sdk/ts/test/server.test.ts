import {create, fromBinary, toBinary} from "@bufbuild/protobuf";
import {Int32ValueSchema, StringValueSchema} from "@bufbuild/protobuf/wkt";
import {describe, expect, inject, it, vi} from "vitest";

import {
    type ClientStatus,
    ErrorCode,
    type IdentityStorage,
    type ModuleView,
    PlayholdClient,
    type SocketLike
} from "../src";

// These tests talk to a real Go server, in internal/testserver, so they check that both sides agree on the wire.
// Its "counter" module adds to a total, and its view shows "<player>:<total>". Rooms have two seats.
const msgAdd = 1;
const msgClose = 2;

let lastCode = 0;
const newCode = (): string => `room-${++lastCode}`;

function memoryStorage(): IdentityStorage {
    const values = new Map<string, string>();
    return {
        getItem: (key) => values.get(key) ?? null,
        setItem: (key, value) => void values.set(key, value),
        removeItem: (key) => void values.delete(key),
    };
}

function viewText(views: ModuleView[]): string {
    const view = views[0];
    if (!view) {
        throw new Error("no view");
    }
    return fromBinary(StringValueSchema, view.view).value;
}

const add = (amount: number): Uint8Array => toBinary(Int32ValueSchema, create(Int32ValueSchema, {value: amount}));

function player(storage: IdentityStorage = memoryStorage(), counterSchema = 2) {
    const seen = {
        view: "",
        status: "disconnected" as ClientStatus,
        errors: [] as ErrorCode[],
        roomClosed: false,
        socket: undefined as SocketLike | undefined,
    };
    const client = new PlayholdClient({
        url: inject("serverUrl"),
        storage,
        schemaVersions: {counter: counterSchema},
        minDelayMs: 10,
        maxDelayMs: 50,
        createSocket: (url) => {
            seen.socket = new WebSocket(url) as unknown as SocketLike;
            return seen.socket;
        },
        onState: (views) => {
            seen.view = viewText(views);
        },
        onStatus: (status) => {
            seen.status = status;
        },
        onError: (error) => seen.errors.push(error.code),
        onRoomClosed: () => {
            seen.roomClosed = true;
        },
    });
    return {client, seen};
}

function joined(code: string, storage?: IdentityStorage) {
    const p = player(storage);
    p.client.join(code);
    p.client.connect();
    return p;
}

const eventually = (check: () => void): Promise<void> => vi.waitFor(check, {timeout: 5000, interval: 10});

describe("against a real server", () => {
    it("tells a client that's too old to update", async () => {
        const {client, seen} = player(memoryStorage(), 1);

        client.connect();

        await eventually(() => expect(seen.status).toBe("updateRequired"));
        expect(seen.errors).toContain(ErrorCode.UPDATE_REQUIRED);
    });

    it("joins, plays, and receives views", async () => {
        const {client, seen} = joined(newCode());

        await eventually(() => expect(seen.status).toBe("joined"));
        await eventually(() => expect(seen.view).toBe(`${client.playerId}:0`));

        client.send("counter", msgAdd, add(3));

        await eventually(() => expect(seen.view).toBe(`${client.playerId}:3`));
        client.close();
    });

    it("shows each player their own view of a shared room", async () => {
        const code = newCode();
        const alice = joined(code);
        const bob = joined(code);
        await eventually(() => expect(bob.seen.status).toBe("joined"));
        await eventually(() => expect(alice.seen.status).toBe("joined"));

        alice.client.send("counter", msgAdd, add(2));

        await eventually(() => expect(alice.seen.view).toBe(`${alice.client.playerId}:2`));
        await eventually(() => expect(bob.seen.view).toBe(`${bob.client.playerId}:2`));
        alice.client.close();
        bob.client.close();
    });

    it("reconnects as the same player, keeping the seat and the game", async () => {
        const {client, seen} = joined(newCode());
        await eventually(() => expect(seen.status).toBe("joined"));
        const id = client.playerId;
        client.send("counter", msgAdd, add(5));
        await eventually(() => expect(seen.view).toBe(`${id}:5`));

        seen.view = "";
        seen.socket?.close();

        await eventually(() => expect(seen.status).toBe("joined"));
        await eventually(() => expect(seen.view).toBe(`${id}:5`));
        expect(client.playerId).toBe(id);
        client.close();
    });

    it("stops the older connection when the same player connects again", async () => {
        const code = newCode();
        const storage = memoryStorage();
        const first = joined(code, storage);
        await eventually(() => expect(first.seen.status).toBe("joined"));

        const second = joined(code, storage);

        await eventually(() => expect(first.seen.status).toBe("replaced"));
        await eventually(() => expect(second.seen.status).toBe("joined"));
        expect(second.client.playerId).toBe(first.seen.view.split(":")[0]);
        second.client.close();
    });

    it("is refused by a full room", async () => {
        const code = newCode();
        const players = [joined(code), joined(code)];
        for (const {seen} of players) {
            await eventually(() => expect(seen.status).toBe("joined"));
        }

        const third = joined(code);

        await eventually(() => expect(third.seen.errors).toContain(ErrorCode.ROOM_FULL));
        expect(third.seen.status).toBe("connected");
        for (const {client} of [...players, third]) {
            client.close();
        }
    });

    it("reports a message the handler refuses, and keeps playing", async () => {
        const {client, seen} = joined(newCode());
        await eventually(() => expect(seen.status).toBe("joined"));

        client.send("counter", msgAdd, add(-1));

        await eventually(() => expect(seen.errors).toContain(ErrorCode.REJECTED));
        expect(seen.status).toBe("joined");
        client.close();
    });

    it("starts as a new guest when the server doesn't know the stored identity", async () => {
        const storage = memoryStorage();
        storage.setItem("playhold.identity", JSON.stringify({playerId: "nobody", secret: "wrong"}));

        const {client, seen} = joined(newCode(), storage);

        await eventually(() => expect(seen.status).toBe("joined"));
        expect(seen.errors).toContain(ErrorCode.UNAUTHORIZED);
        expect(client.playerId).not.toBe("nobody");
        client.close();
    });

    it("tells the player when the room closes", async () => {
        const {client, seen} = joined(newCode());
        await eventually(() => expect(seen.status).toBe("joined"));

        client.send("counter", msgClose, new Uint8Array());

        await eventually(() => expect(seen.roomClosed).toBe(true));
        expect(seen.status).toBe("connected");
        client.close();
    });
});