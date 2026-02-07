
import random
from colorama import Fore, Back
list_of_words = [
    "water",
    "ditto",
    "video",
    "audio",
    "witch",
    "magic",
    "prime",
    "bread",
    "debit",
    "vodka",
    "false",
    "touch",
    "happy",
    "check",
    "apple",
    "watch",
    "touch",
    "cable",
    "mouse",
    "phone",
    "month",
    "dairy",
    "graph",
    "photo",
    "lower",
    "upper",
    "penny",
]

logo = """
                        .___.__          
__  _  _____________  __| _/|  |   ____  
\ \/ \/ /  _ \_  __ \/ __ | |  | _/ __ \ 
 \     (  <_> )  | \/ /_/ | |  |_\  ___/ 
  \/\_/ \____/|__|  \____ | |____/\___  >
                         \/           \/ 

"""
print(logo)

answer = random.choice(list_of_words)

print(answer)

attemptsRemaining = 6
endOfGame = False

while not endOfGame and attemptsRemaining > 0:

    user = input("Guess your word: ").lower()

    for check in range(len(user)):
        if user[check] == answer[check]:
            fAnswer = Fore.GREEN + user[check]
        elif user[check] in answer[check]:
            fAnswer = Fore.YELLOW + user[check]
            attemptsRemaining -= 1
        elif user[check] not in answer[check]:
            fAnswer = Fore.RED + user[check]
            attemptsRemaining -= 1
        print(fAnswer)
    print(f"You have {attemptsRemaining} attempts")

    if user == answer:
        print(Fore.GREEN + "Congratulations!!! You guessed the word!")
        endOfGame = True
    elif attemptsRemaining == 0:
        print(
            Fore.RED + f"Sorry, You ran out of attempts. The word was:, {answer}")
        endOfGame = True
