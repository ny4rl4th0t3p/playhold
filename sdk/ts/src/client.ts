import { create, fromBinary, toBinary, type MessageInitShape } from "@bufbuild/protobuf";

import {
  ClientFrameSchema,
  CloseReason,
  ErrorCode,
  ServerFrameSchema,
  type Error as ServerError,
  type ModuleView,
  type ServerFrame,
} from "./gen/playhold/v1/frames_pb";
import { defaultStorage, IdentityStore, type IdentityStorage } from "./identity";

const protocolVersion = 1;
/** The close status of a connection replaced by a newer one from the same player. */
const statusReplaced = 4001;
const statusNormal = 1000;
const defaultMinDelayMs = 500;
const defaultMaxDelayMs = 15_000;

/** The part of the browser's WebSocket this SDK uses. */
export interface SocketLike {
  binaryType: string;
  onopen: (() => void) | null;
  onmessage: ((event: { data: unknown }) => void) | null;
  onclose: ((event: { code: number }) => void) | null;
  send(data: Uint8Array): void;
  close(code?: number, reason?: string): void;
}

/**
 * - `disconnected`: not connected, and retrying unless `connect` hasn't been called yet.
 * - `connecting`: opening a connection and saying hello.
 * - `connected`: the server knows this player, who isn't in a room.
 * - `joined`: in a room, and messages can be sent.
 * - `replaced`: this player connected somewhere newer, such as another tab. It stays stopped until `connect`.
 * - `closed`: stopped by `close`.
 * - `updateRequired`: this client is too old for the server, and has stopped. The player needs a newer one, such as by
 *   reloading the page.
 */
export type ClientStatus = "disconnected" | "connecting" | "connected" | "joined" | "replaced" | "closed" | "updateRequired";

export interface ClientHandlers {
  onStatus?(status: ClientStatus): void;
  /** Called with the player's full view of the room. Each view's bytes are that module's own message. */
  onState?(views: ModuleView[]): void;
  /** Called when the server reports a problem. `seq` is the number `send` returned. */
  onError?(error: { seq: number; code: ErrorCode; message: string }): void;
  /** Called when the player's room has closed. The client stays connected and can join another room. */
  onRoomClosed?(reason: CloseReason): void;
  /** Called each time the client enters its room, with the room's code: for `createRoom`, the one the server made. */
  onJoined?(code: string): void;
}

export interface ClientOptions extends ClientHandlers {
  url: string;
  /** Which version of each module's messages this client was built for, so the server can tell if it's too old. */
  schemaVersions?: Record<string, number>;
  /** Where the guest's identity is kept. The default is localStorage, shared by this browser's tabs. */
  storage?: IdentityStorage;
  storageKey?: string;
  /** The delay before the first retry, doubling up to maxDelayMs. */
  minDelayMs?: number;
  maxDelayMs?: number;
  createSocket?: (url: string) => SocketLike;
  random?: () => number;
}

/**
 * Connects a game to a playhold server: guest identity, joining a room, sending messages, receiving views, and
 * reconnecting after a dropped connection. Messages sent while disconnected aren't queued or resent. A returning
 * player receives a full view, and acts on what it shows.
 */
export class PlayholdClient {
  private readonly identity: IdentityStore;
  private readonly createSocket: (url: string) => SocketLike;
  private readonly random: () => number;
  private readonly minDelayMs: number;
  private readonly maxDelayMs: number;

  private socket: SocketLike | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private failedAttempts = 0;
  private lastSeq = 0;
  private code: string | undefined;
  private joinSeq: number | undefined;
  private currentStatus: ClientStatus = "disconnected";
  private currentPlayerId: string | undefined;

  constructor(private readonly options: ClientOptions) {
    this.identity = new IdentityStore(options.storage ?? defaultStorage(), options.storageKey ?? "playhold.identity");
    this.createSocket = options.createSocket ?? ((url) => new WebSocket(url) as unknown as SocketLike);
    this.random = options.random ?? Math.random;
    this.minDelayMs = options.minDelayMs ?? defaultMinDelayMs;
    this.maxDelayMs = options.maxDelayMs ?? defaultMaxDelayMs;
  }

  get status(): ClientStatus {
    return this.currentStatus;
  }

  /** The player's ID, once the server has accepted them. */
  get playerId(): string | undefined {
    return this.currentPlayerId;
  }

  /** Starts connecting, and keeps reconnecting until `close`. After `replaced`, this takes the player back. */
  connect(): void {
    if (this.socket || this.timer !== undefined) {
      return;
    }
    this.open();
  }

  /**
   * Enters the room with this code, now or as soon as the client is connected. A code no room has creates one only on
   * a server that allows codes clients choose; otherwise it's refused with `ROOM_NOT_FOUND`.
   */
  join(code: string): void {
    if (this.code === code) {
      return;
    }
    if (this.code !== undefined) {
      throw new Error("already in a room");
    }
    this.code = code;
    if (this.currentStatus === "connected") {
      this.sendJoin();
    }
  }

  /**
   * Creates a room, with a code the server makes, and enters it, now or as soon as the client is connected. The code
   * arrives in `onJoined`, to share with the other players.
   */
  createRoom(): void {
    this.join(""); // an empty code asks the server for a new room
  }

  /**
   * Sends a message to one of the room's modules. It returns the frame's number, which appears in any error about
   * it, or undefined if the client isn't in a room, in which case nothing was sent.
   */
  send(module: string, id: number, payload: Uint8Array): number | undefined {
    if (this.currentStatus !== "joined") {
      return undefined;
    }
    return this.sendFrame({ case: "message", value: { module, id, payload } });
  }

  /** Disconnects and stops reconnecting. The guest's identity is kept for next time. */
  close(): void {
    this.stop("closed");
  }

  private open(): void {
    this.timer = undefined;
    this.setStatus("connecting");

    const socket = this.createSocket(this.options.url);
    socket.binaryType = "arraybuffer";
    socket.onopen = () => {
      if (socket === this.socket) {
        this.sendHello();
      }
    };
    socket.onmessage = (event) => {
      if (socket === this.socket) {
        this.receive(event.data);
      }
    };
    socket.onclose = (event) => {
      if (socket === this.socket) {
        this.closed(event.code);
      }
    };
    this.socket = socket;
  }

  private closed(status: number): void {
    this.socket = undefined;
    this.currentPlayerId = undefined;
    if (status === statusReplaced) {
      this.setStatus("replaced");
      return;
    }
    this.setStatus("disconnected");

    // Half random, so players dropped together don't all come back at the same moment.
    const delay = Math.min(this.maxDelayMs, this.minDelayMs * 2 ** this.failedAttempts);
    this.failedAttempts++;
    this.timer = setTimeout(() => this.open(), delay * (0.5 + 0.5 * this.random()));
  }

  private stop(status: ClientStatus): void {
    clearTimeout(this.timer);
    this.timer = undefined;
    const socket = this.socket;
    this.socket = undefined;
    socket?.close(statusNormal);
    this.setStatus(status);
  }

  private receive(data: unknown): void {
    if (!(data instanceof ArrayBuffer)) {
      return;
    }
    let frame: ServerFrame;
    try {
      frame = fromBinary(ServerFrameSchema, new Uint8Array(data));
    } catch {
      this.options.onError?.({ seq: 0, code: ErrorCode.UNSPECIFIED, message: "frame could not be decoded" });
      return;
    }

    const body = frame.body;
    switch (body.case) {
      case "welcome":
        this.welcomed(body.value.playerId, body.value.secret);
        break;
      case "joined":
        this.joinSeq = undefined;
        this.code = body.value.code; // the server's, for a room it made, so a reconnect comes back to this room
        this.setStatus("joined");
        this.options.onJoined?.(body.value.code);
        break;
      case "state":
        this.options.onState?.(body.value.views);
        break;
      case "error":
        this.failed(body.value);
        break;
      case "closed":
        this.code = undefined;
        this.setStatus("connected");
        this.options.onRoomClosed?.(body.value.reason);
        break;
    }
  }

  private welcomed(playerId: string, secret: string): void {
    this.failedAttempts = 0;
    this.currentPlayerId = playerId;
    if (secret !== "") {
      this.identity.save({ playerId, secret });
    }
    this.setStatus("connected");
    if (this.code !== undefined) {
      this.sendJoin();
    }
  }

  private failed(error: ServerError): void {
    const seq = Number(error.seq);
    if (error.code === ErrorCode.UNAUTHORIZED) {
      // The server has forgotten this guest, for example after a restart. Start again as a new one.
      this.identity.clear();
      this.sendHello();
    } else if (error.code === ErrorCode.UNSUPPORTED_PROTOCOL || error.code === ErrorCode.UPDATE_REQUIRED) {
      this.stop("updateRequired");
    } else if (
      error.code === ErrorCode.UNAVAILABLE ||
      (error.code === ErrorCode.RATE_LIMITED && (this.currentPlayerId === undefined || seq === this.joinSeq))
    ) {
      // The server couldn't check this player, or asked it to slow down before it got into its room. Closing leads to
      // the usual reconnect, with its growing delay, and keeps the room's code to try again.
      this.socket?.close();
    } else if (seq === this.joinSeq) {
      // Forget the code, so a reconnect doesn't keep trying a room that refused this player.
      this.code = undefined;
      this.joinSeq = undefined;
    }
    this.options.onError?.({ seq, code: error.code, message: error.message });
  }

  private sendHello(): void {
    const identity = this.identity.load();
    this.sendFrame({
      case: "hello",
      value: {
        protocolVersion,
        playerId: identity?.playerId ?? "",
        secret: identity?.secret ?? "",
        schemaVersions: this.options.schemaVersions ?? {},
      },
    });
  }

  private sendJoin(): void {
    this.joinSeq = this.sendFrame({ case: "join", value: { code: this.code ?? "" } });
  }

  private sendFrame(body: NonNullable<MessageInitShape<typeof ClientFrameSchema>["body"]>): number {
    this.lastSeq++;
    const frame = create(ClientFrameSchema, { seq: BigInt(this.lastSeq), body });
    this.socket?.send(toBinary(ClientFrameSchema, frame));
    return this.lastSeq;
  }

  private setStatus(status: ClientStatus): void {
    if (status !== this.currentStatus) {
      this.currentStatus = status;
      this.options.onStatus?.(status);
    }
  }
}