import sys
import requests
import dotenv

config = dotenv.dotenv_values(sys.argv[1])

DISCORD_API = 'https://discord.com/api'
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

requests.post(
    DISCORD_API_SYNC_COMMANDS,
    headers={'Authorization': f'Bot {config["BOT_TOKEN"]}'},
    json={
        'name': 'skip',
        'type': 1,
        'description': 'Skip current song'})
