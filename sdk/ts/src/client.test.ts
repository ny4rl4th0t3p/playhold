import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PlayholdClient, type ClientOptions, type SocketLike } from "./client";
import {
  ClientFrameSchema,
  CloseReason,
  ErrorCode,
  ServerFrameSchema,
  type ClientFrame,
} from "./gen/playhold/v1/frames_pb";
import type { IdentityStorage } from "./identity";

const statusReplaced = 4001;
const statusAbnormal = 1006;

class FakeSocket implements SocketLike {
  binaryType = "";
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: unknown }) => void) | null = null;
  onclose: ((event: { code: number }) => void) | null = null;
  sent: ClientFrame[] = [];
  closedWith: number | undefined;

  send(data: Uint8Array): void {
    this.sent.push(fromBinary(ClientFrameSchema, data));
  }

  close(code?: number): void {
    this.closedWith = code;
  }

  receive(body: NonNullable<MessageInitShape<typeof ServerFrameSchema>["body"]>): void {
    const bytes = toBinary(ServerFrameSchema, create(ServerFrameSchema, { body }));
    this.onmessage?.({ data: bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength) });
  }

  welcome(playerId = "alice", secret = ""): void {
    this.receive({ case: "welcome", value: { playerId, secret } });
  }

  drop(code = statusAbnormal): void {
    this.onclose?.({ code });
  }

  last(): ClientFrame {
    const frame = this.sent.at(-1);
    if (!frame) {
      throw new Error("nothing was sent");
    }
    return frame;
  }
}

function memoryStorage(initial: Record<string, string> = {}): IdentityStorage {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => void values.set(key, value),
    removeItem: (key) => void values.delete(key),
  };
}

const blockedStorage: IdentityStorage = {
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
  removeItem: () => {
    throw new Error("blocked");
  },
};

function setup(options: Partial<ClientOptions> = {}) {
  const sockets: FakeSocket[] = [];
  const client = new PlayholdClient({
    url: "ws://test",
    storage: memoryStorage(),
    random: () => 1, // no jitter, so delays are exact
    minDelayMs: 100,
    maxDelayMs: 1000,
    createSocket: () => {
      const socket = new FakeSocket();
      sockets.push(socket);
      return socket;
    },
    ...options,
  });
  const latest = (): FakeSocket => {
    const socket = sockets.at(-1);
    if (!socket) {
      throw new Error("no socket was created");
    }
    return socket;
  };
  /** Connects and says hello, leaving the client in `connected`. */
  const online = (): FakeSocket => {
    client.connect();
    const socket = latest();
    socket.onopen?.();
    socket.welcome();
    return socket;
  };
  /** Also joins a room, leaving the client in `joined`. */
  const inRoom = (code = "room"): FakeSocket => {
    const socket = online();
    client.join(code);
    socket.receive({ case: "joined", value: { seq: socket.last().seq, code } });
    return socket;
  };
  return { client, sockets, latest, online, inRoom };
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("identity", () => {
  it("says hello as a new guest and stores the identity it receives", () => {
    const storage = memoryStorage();
    const { client, latest } = setup({ storage });

    client.connect();
    latest().onopen?.();

    const hello = latest().last().body;
    expect(hello.case).toBe("hello");
    expect(hello.value).toMatchObject({ protocolVersion: 1, playerId: "", secret: "" });

    latest().welcome("alice", "s3cret");

    expect(client.playerId).toBe("alice");
    expect(JSON.parse(storage.getItem("playhold.identity") ?? "")).toEqual({ playerId: "alice", secret: "s3cret" });
  });

  it("says hello with a stored identity, and keeps it when no new secret arrives", () => {
    const stored = JSON.stringify({ playerId: "alice", secret: "s3cret" });
    const storage = memoryStorage({ "playhold.identity": stored });
    const { client, latest } = setup({ storage });

    client.connect();
    latest().onopen?.();

    expect(latest().last().body.value).toMatchObject({ playerId: "alice", secret: "s3cret" });

    latest().welcome("alice", "");

    expect(storage.getItem("playhold.identity")).toBe(stored);
  });

  it.each(["not json", "null", "42", '{"playerId":"alice"}'])("ignores a corrupted identity: %s", (raw) => {
    const { client, latest } = setup({ storage: memoryStorage({ "playhold.identity": raw }) });

    client.connect();
    latest().onopen?.();

    expect(latest().last().body.value).toMatchObject({ playerId: "", secret: "" });
  });

  it("starts again as a new guest when the server has forgotten this one", () => {
    const stored = JSON.stringify({ playerId: "alice", secret: "old" });
    const storage = memoryStorage({ "playhold.identity": stored });
    const onError = vi.fn();
    const { client, latest } = setup({ storage, onError });

    client.connect();
    latest().onopen?.();
    latest().receive({ case: "error", value: { seq: 1n, code: ErrorCode.UNAUTHORIZED, message: "no" } });

    expect(storage.getItem("playhold.identity")).toBeNull();
    expect(latest().last().body.value).toMatchObject({ playerId: "", secret: "" });
    expect(onError).toHaveBeenCalledOnce();
    expect(client.status).toBe("connecting");
  });

  it("survives a dropped connection in memory when storage is blocked", () => {
    const { client, latest } = setup({ storage: blockedStorage });

    client.connect();
    latest().onopen?.();
    latest().welcome("alice", "s3cret");
    latest().drop();
    vi.advanceTimersByTime(100);
    latest().onopen?.();

    expect(latest().last().body.value).toMatchObject({ playerId: "alice", secret: "s3cret" });
  });
});

describe("joining", () => {
  it("joins as soon as it's connected, whichever comes first", () => {
    const early = setup();
    early.client.join("room");
    early.client.connect();
    early.latest().onopen?.();
    early.latest().welcome();
    expect(early.latest().last().body).toMatchObject({ case: "join", value: { code: "room" } });
    expect(early.client.status).toBe("connected");

    const late = setup();
    const socket = late.online();
    late.client.join("room");
    expect(socket.last().body).toMatchObject({ case: "join", value: { code: "room" } });
  });

  it("becomes joined when the server accepts", () => {
    const { client, inRoom } = setup();

    inRoom();

    expect(client.status).toBe("joined");
  });

  it("creates a room with the server's code, reports it, and comes back to it after reconnecting", () => {
    const onJoined = vi.fn();
    const { client, latest, online } = setup({ onJoined });
    const socket = online();

    client.createRoom();
    expect(socket.last().body).toMatchObject({ case: "join", value: { code: "" } });
    socket.receive({ case: "joined", value: { seq: socket.last().seq, code: "MADEBYTHESERVER" } });
    expect(client.status).toBe("joined");
    expect(onJoined).toHaveBeenCalledWith("MADEBYTHESERVER");

    latest().drop();
    vi.advanceTimersByTime(100);
    latest().onopen?.();
    latest().welcome();
    expect(latest().last().body).toMatchObject({ case: "join", value: { code: "MADEBYTHESERVER" } });
  });

  it("refuses a second room, but allows repeating the same code", () => {
    const { client, inRoom } = setup();
    const socket = inRoom("room");
    const sent = socket.sent.length;

    client.join("room");
    expect(socket.sent).toHaveLength(sent);

    expect(() => client.join("other")).toThrow("already in a room");
  });

  it("reports a refused join, forgets the code, and can join another room", () => {
    const onError = vi.fn();
    const { client, online, latest } = setup({ onError });
    const socket = online();

    client.join("full");
    socket.receive({ case: "error", value: { seq: socket.last().seq, code: ErrorCode.ROOM_FULL, message: "full" } });

    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ code: ErrorCode.ROOM_FULL }));
    expect(client.status).toBe("connected");

    // A reconnect doesn't retry the room that refused.
    socket.drop();
    vi.advanceTimersByTime(100);
    latest().onopen?.();
    latest().welcome();
    expect(latest().last().body.case).toBe("hello");

    client.join("other");
    expect(latest().last().body).toMatchObject({ case: "join", value: { code: "other" } });
  });

  it("keeps the code and tries again later when a join is refused for coming too fast", () => {
    const onError = vi.fn();
    const { client, online, latest } = setup({ onError });
    const socket = online();

    client.join("room");
    socket.receive({ case: "error", value: { seq: socket.last().seq, code: ErrorCode.RATE_LIMITED, message: "slow down" } });

    expect(onError).toHaveBeenCalledWith(expect.objectContaining({ code: ErrorCode.RATE_LIMITED }));
    socket.drop();
    vi.advanceTimersByTime(100);
    latest().onopen?.();
    latest().welcome();
    expect(latest().last().body).toMatchObject({ case: "join", value: { code: "room" } });
  });

  it("can join another room after its room closes", () => {
    const onRoomClosed = vi.fn();
    const { client, inRoom } = setup({ onRoomClosed });
    const socket = inRoom("room");

    socket.receive({ case: "closed", value: { reason: CloseReason.STUCK } });

    expect(onRoomClosed).toHaveBeenCalledWith(CloseReason.STUCK);
    expect(client.status).toBe("connected");

    client.join("next");
    expect(socket.last().body).toMatchObject({ case: "join", value: { code: "next" } });
  });
});

describe("messages and views", () => {
  it("doesn't send before joining, and doesn't queue", () => {
    const { client, online } = setup();

    expect(client.send("deck", 1, new Uint8Array())).toBeUndefined();

    const socket = online();
    const sent = socket.sent.length;
    expect(client.send("deck", 1, new Uint8Array())).toBeUndefined();
    expect(socket.sent).toHaveLength(sent);
  });

  it("sends module messages with increasing numbers", () => {
    const { client, inRoom } = setup();
    const socket = inRoom();

    const first = client.send("deck", 1, new Uint8Array([1, 2]));
    const second = client.send("deck", 2, new Uint8Array());

    expect(socket.sent.at(-2)?.seq).toBe(BigInt(first ?? 0));
    expect(socket.sent.at(-2)?.body).toMatchObject({
      case: "message",
      value: { module: "deck", id: 1, payload: new Uint8Array([1, 2]) },
    });
    expect(second).toBe((first ?? 0) + 1);
  });

  it("passes on room state", () => {
    const onState = vi.fn();
    const { inRoom } = setup({ onState });
    const socket = inRoom();

    socket.receive({
      case: "state",
      value: { views: [{ module: "deck", view: new Uint8Array([9]) }] },
    });

    expect(onState).toHaveBeenCalledOnce();
    expect(onState.mock.calls[0]?.[0][0]).toMatchObject({ module: "deck", view: new Uint8Array([9]) });
  });

  it("reports errors about a message with its number and stays joined", () => {
    const onError = vi.fn();
    const { client, inRoom } = setup({ onError });
    const socket = inRoom();

    const seq = client.send("deck", 1, new Uint8Array());
    socket.receive({
      case: "error",
      value: { seq: BigInt(seq ?? 0), code: ErrorCode.REJECTED, message: "not your turn" },
    });

    expect(onError).toHaveBeenCalledWith({ seq, code: ErrorCode.REJECTED, message: "not your turn" });
    expect(client.status).toBe("joined");
  });

  it("reports a message refused for coming too fast and stays joined", () => {
    const onError = vi.fn();
    const { client, inRoom } = setup({ onError });
    const socket = inRoom();

    const seq = client.send("chat", 1, new Uint8Array());
    socket.receive({
      case: "error",
      value: { seq: BigInt(seq ?? 0), code: ErrorCode.RATE_LIMITED, message: "slow down" },
    });

    expect(onError).toHaveBeenCalledWith({ seq, code: ErrorCode.RATE_LIMITED, message: "slow down" });
    expect(client.status).toBe("joined");
    expect(socket.closedWith).toBeUndefined();
  });

  it("reports a frame it can't decode and carries on", () => {
    const onError = vi.fn();
    const { client, inRoom } = setup({ onError });
    const socket = inRoom();

    socket.onmessage?.({ data: new Uint8Array([0xff]).buffer });

    expect(onError).toHaveBeenCalledOnce();
    expect(client.status).toBe("joined");
  });

  it("asks for binary frames", () => {
    const { client, latest } = setup();

    client.connect();

    expect(latest().binaryType).toBe("arraybuffer");
  });
});

describe("reconnecting", () => {
  it("backs off, doubling up to the maximum", () => {
    const { client, sockets, latest } = setup();
    client.connect();

    for (const delay of [100, 200, 400, 800, 1000, 1000]) {
      const before = sockets.length;
      latest().drop();
      expect(client.status).toBe("disconnected");

      vi.advanceTimersByTime(delay - 1);
      expect(sockets).toHaveLength(before);

      vi.advanceTimersByTime(1);
      expect(sockets).toHaveLength(before + 1);
    }
  });

  it("randomizes the delay within its upper half", () => {
    const { client, sockets, latest } = setup({ random: () => 0 });
    client.connect();

    latest().drop();
    vi.advanceTimersByTime(49);
    expect(sockets).toHaveLength(1);

    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);
  });

  it("only resets the backoff once the server accepts the player", () => {
    const { sockets, latest, online } = setup();
    online();

    latest().drop();
    vi.advanceTimersByTime(100);
    latest().onopen?.(); // opens, but no welcome
    latest().drop();

    vi.advanceTimersByTime(199);
    expect(sockets).toHaveLength(2);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(3);

    latest().onopen?.();
    latest().welcome();
    latest().drop();
    vi.advanceTimersByTime(100);
    expect(sockets).toHaveLength(4);
  });

  it("rejoins its room after reconnecting", () => {
    const { client, latest, inRoom } = setup();
    inRoom("room");

    latest().drop();
    expect(client.status).toBe("disconnected");
    expect(client.playerId).toBeUndefined();

    vi.advanceTimersByTime(100);
    latest().onopen?.();
    latest().welcome();

    expect(latest().last().body).toMatchObject({ case: "join", value: { code: "room" } });
    latest().receive({ case: "joined", value: { seq: latest().last().seq, code: "room" } });
    expect(client.status).toBe("joined");
  });

  it("doesn't resend messages", () => {
    const { client, latest, inRoom } = setup();
    inRoom();
    client.send("deck", 1, new Uint8Array());

    latest().drop();
    vi.advanceTimersByTime(100);
    latest().onopen?.();
    latest().welcome();

    expect(latest().sent.map((frame) => frame.body.case)).toEqual(["hello", "join"]);
  });

  it("doesn't reconnect after being replaced, until asked", () => {
    const { client, sockets, latest, inRoom } = setup();
    inRoom();

    latest().drop(statusReplaced);
    expect(client.status).toBe("replaced");
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);

    client.connect();
    expect(sockets).toHaveLength(2);
    latest().onopen?.();
    latest().welcome();
    expect(latest().last().body).toMatchObject({ case: "join", value: { code: "room" } });
  });

  it("stops when closed", () => {
    const { client, sockets, latest, inRoom } = setup();
    const socket = inRoom();

    client.close();
    expect(client.status).toBe("closed");
    expect(socket.closedWith).toBe(1000);

    latest().drop();
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
  });

  it("cancels a pending retry when closed", () => {
    const { client, sockets, latest } = setup();
    client.connect();
    latest().drop();

    client.close();
    vi.advanceTimersByTime(60_000);

    expect(sockets).toHaveLength(1);
  });

  it.each([ErrorCode.UNSUPPORTED_PROTOCOL, ErrorCode.UPDATE_REQUIRED])("stops when it's too old for the server: %s", (code) => {
    const onError = vi.fn();
    const stored = JSON.stringify({ playerId: "alice", secret: "s3cret" });
    const storage = memoryStorage({ "playhold.identity": stored });
    const { client, sockets, latest } = setup({ onError, storage });
    client.connect();
    latest().onopen?.();

    latest().receive({ case: "error", value: { seq: 1n, code, message: "too old" } });

    expect(client.status).toBe("updateRequired");
    expect(onError).toHaveBeenCalledOnce();
    expect(storage.getItem("playhold.identity")).toBe(stored);
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(1);
  });

  it("says which versions it was built for", () => {
    const { client, latest } = setup({ schemaVersions: { deck: 2, hands: 5 } });

    client.connect();
    latest().onopen?.();

    expect(latest().last().body.value).toMatchObject({ schemaVersions: { deck: 2, hands: 5 } });
  });

  it.each([ErrorCode.UNAVAILABLE, ErrorCode.RATE_LIMITED])("backs off and keeps its identity when told to wait: %s", (code) => {
    const stored = JSON.stringify({ playerId: "alice", secret: "s3cret" });
    const storage = memoryStorage({ "playhold.identity": stored });
    const onError = vi.fn();
    const { client, sockets, latest } = setup({ storage, onError });
    client.connect();
    latest().onopen?.();

    latest().receive({ case: "error", value: { seq: 1n, code, message: "try later" } });

    expect(onError).toHaveBeenCalledOnce();
    expect(latest().closedWith).toBeUndefined();
    expect(storage.getItem("playhold.identity")).toBe(stored);

    latest().drop();
    vi.advanceTimersByTime(100);
    expect(sockets).toHaveLength(2);
  });

  it("doesn't open a second connection when already connecting or waiting", () => {
    const { client, sockets, latest } = setup();

    client.connect();
    client.connect();
    expect(sockets).toHaveLength(1);

    latest().drop();
    client.connect();
    expect(sockets).toHaveLength(1);
  });

  it("ignores events from a socket it has moved on from", () => {
    const onState = vi.fn();
    const { client, sockets, latest, inRoom } = setup({ onState });
    const old = inRoom();

    old.drop();
    vi.advanceTimersByTime(100);
    const current = latest();
    current.onopen?.();

    old.receive({ case: "state", value: { views: [] } });
    old.drop();

    expect(onState).not.toHaveBeenCalled();
    expect(client.status).toBe("connecting");
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(2);
  });
});

describe("status", () => {
  it("reports each change once", () => {
    const onStatus = vi.fn();
    const { inRoom } = setup({ onStatus });

    inRoom();

    expect(onStatus.mock.calls.map(([status]) => status)).toEqual(["connecting", "connected", "joined"]);
  });
});