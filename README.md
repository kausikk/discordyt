TODO:
- unhandlded json encode/decodes
- change consts and struct naming
- send channel leave when startVoiceGw fails?
- rename *_consts.go files
- add state handling to the start of connect, listen, reconnect, playaudio, join channel

Done:
- handle INVALID_SESSION opcode -> mostly done
- handle non-existing GuildStates in handle dispatch -> i think my solution is ok
- unclear what happens when user manually drags bot into a different channel
    - when is voice server update event sent? -> when moved to a different channel, either by moderator or by bot itself
    - when should voice gateway be reconnected (not resumed)? -> after a pair of voice server/state update events are received with a non-nil channel
    - what session id, token, and url should voice gateway use on resume/reconnect? -> same session id, token, and url used on first connect
- add Gateway Reconnect that preserves guild states -> done
- add Gateway Close -> done
- add SIGINT handling (need to close websockets) -> done

Ideas:
- Connect and Listen should be combined (Launch) -> No
- Voice gateways might not need to be closed if Gateway relaunches?
