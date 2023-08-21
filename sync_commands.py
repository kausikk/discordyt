import sys
import requests

import dotenv

config = dotenv.dotenv_values(sys.argv[1])

DISCORD_API = 'https://discord.com/api'
DISCORD_API_GET_GATEWAY = f'{DISCORD_API}/gateway'
DISCORD_API_SYNC_COMMANDS = f'{DISCORD_API}/applications/{config["BOT_APP_ID"]}/commands'
DISCORD_API_DELETE_COMMANDS = f'{DISCORD_API}/applications/{config["BOT_APP_ID"]}/commands'

requests.post(
    DISCORD_API_SYNC_COMMANDS,
    headers={'Authorization': f'Bot {config["BOT_TOKEN"]}'},
    json={
        'name': 'play',
        'type': 1,
        'description': 'Play a song from Youtube',
        'options': [{
                'name': 'query',
                'description': 'Keywords to search for video',
                'type': 3,
                'required': True}]})

requests.post(
    DISCORD_API_SYNC_COMMANDS,
    headers={'Authorization': f'Bot {config["BOT_TOKEN"]}'},
    json={
        'name': 'stop',
        'type': 1,
        'description': 'Stop song and leave channel'})

# DISCORD_API_SYNC_GUILD_COMMANDS = f'{DISCORD_API}/applications/{config["BOT_APP_ID"]}/guilds/{config["GUILD_ID"]}/commands'
# DISCORD_API_DELETE_GUILD_COMMANDS = f'{DISCORD_API}/applications/{config["BOT_APP_ID"]}/guilds/{config["GUILD_ID"]}/commands'

# result = requests.delete(
#     f'{DISCORD_API_SYNC_COMMANDS}/1132860391295295498',
#     headers={'Authorization': f'Bot {config["BOT_TOKEN"]}'})
# print(result.text)

# result = requests.delete(
#     f'{DISCORD_API_SYNC_COMMANDS}/1132860393107238983',
#     headers={'Authorization': f'Bot {config["BOT_TOKEN"]}'})
# print(result.text)

# result = requests.get(
#     DISCORD_API_GET_GATEWAY,
#     headers={'User Agent': f'yt-bot ({config["BOT_URL"]}, v0.1)'}
# )
# print(result.text)
