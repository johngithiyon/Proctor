from flask import Flask, request, jsonify
import cv2
import numpy as np
import base64
import os
import time

app = Flask(__name__)

os.makedirs("captured_images", exist_ok=True)
reference_face_path = None

def save_image_from_base64(data_url):
    parts = data_url.split(",")
    if len(parts) != 2:
        return None
    decoded = base64.b64decode(parts[1])
    timestamp = time.strftime("%Y%m%d_%H%M%S")
    filename = f"captured_images/{timestamp}.png"
    with open(filename, "wb") as f:
        f.write(decoded)
    return filename

def compare_faces(ref_path, curr_path):
    ref_img = cv2.imread(ref_path)
    curr_img = cv2.imread(curr_path)
    if ref_img is None or curr_img is None:
        return False

    ref_gray = cv2.cvtColor(ref_img, cv2.COLOR_BGR2GRAY)
    curr_gray = cv2.cvtColor(curr_img, cv2.COLOR_BGR2GRAY)

    res = cv2.matchTemplate(curr_gray, ref_gray, cv2.TM_CCOEFF_NORMED)
    _, max_val, _, _ = cv2.minMaxLoc(res)

    return max_val >= 0.6

@app.route("/capture", methods=["POST"])
def capture():
    global reference_face_path
    img_data = request.form.get("image")
    username = request.form.get("username")
    noise_violation = request.form.get("noise_violation")

    if not img_data:
        return "ERROR", 400

    curr_path = save_image_from_base64(img_data)
    if reference_face_path is None:
        reference_face_path = curr_path
        return "OK"

    # 1. Check for face mismatch first
    if not compare_faces(reference_face_path, curr_path):
        return "FACE_MISMATCH"

    # 2. Then, check for noise violation
    # The frontend sends "true" or "false" as a string
    if noise_violation == "true":
        return "NOISE_VIOLATION"

    # If both checks pass
    return "OK"

if __name__ == "__main__":
    app.run(port=5000)